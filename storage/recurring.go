package storage

import (
	"database/sql"
	"time"
)

// RecurringType defines the type of recurrence
type RecurringType string

const (
	RecurringDaily   RecurringType = "daily"
	RecurringWeekly  RecurringType = "weekly"
	RecurringMonthly RecurringType = "monthly"
)

// RecurringReminder represents a recurring reminder
type RecurringReminder struct {
	ID            int64
	ChatID        int64
	UserID        int64
	Label         string
	CreatedAt     time.Time
	RecurringType RecurringType
	Time          string // Time of day in format "15:04"
	DayOfWeek     int    // 0-6 for weekly reminders (0 = Sunday)
	DayOfMonth    int    // 1-31 for monthly reminders
	LastTriggered time.Time
	Active        bool
}

// AddRecurringReminder adds a new recurring reminder
func (r *ReminderRepository) AddRecurringReminder(
	chatID, userID int64,
	label string,
	recurringType RecurringType,
	timeStr string,
	dayOfWeek, dayOfMonth int) (int64, error) {

	r.lock.Lock()
	defer r.lock.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	result, err := tx.Exec(
		`INSERT INTO recurring_reminders (
			chat_id, user_id, label, created_at, 
			recurring_type, time, day_of_week, day_of_month, active
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		chatID,
		userID,
		label,
		time.Now(),
		string(recurringType),
		timeStr,
		sql.NullInt64{Int64: int64(dayOfWeek), Valid: dayOfWeek >= 0},
		sql.NullInt64{Int64: int64(dayOfMonth), Valid: dayOfMonth > 0},
	)

	if err != nil {
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}

	return id, nil
}

// GetUserRecurringReminders gets all active recurring reminders for a user
func (r *ReminderRepository) GetUserRecurringReminders(userID int64) ([]RecurringReminder, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	query := `
	SELECT id, chat_id, user_id, label, created_at, recurring_type, 
	       time, IFNULL(day_of_week, -1), IFNULL(day_of_month, -1), 
	       IFNULL(last_triggered, '1970-01-01 00:00:00'), active
	FROM recurring_reminders
	WHERE user_id = ? AND active = 1
	ORDER BY created_at DESC
	`

	rows, err := r.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []RecurringReminder
	for rows.Next() {
		var r RecurringReminder
		var recurringTypeStr string

		err := rows.Scan(
			&r.ID, &r.ChatID, &r.UserID, &r.Label, &r.CreatedAt,
			&recurringTypeStr, &r.Time, &r.DayOfWeek, &r.DayOfMonth,
			&r.LastTriggered, &r.Active,
		)

		if err != nil {
			return nil, err
		}

		r.RecurringType = RecurringType(recurringTypeStr)
		reminders = append(reminders, r)
	}

	return reminders, nil
}

// UpdateRecurringReminderLastTriggered updates the last_triggered timestamp
func (r *ReminderRepository) UpdateRecurringReminderLastTriggered(id int64, lastTriggered time.Time) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	_, err := r.db.Exec("UPDATE recurring_reminders SET last_triggered = ? WHERE id = ?", lastTriggered, id)
	return err
}

// UpdateRecurringReminder updates a recurring reminder
func (r *ReminderRepository) UpdateRecurringReminder(id, userID int64, label string,
	recurringType RecurringType, timeStr string, dayOfWeek, dayOfMonth int) (bool, error) {

	r.lock.Lock()
	defer r.lock.Unlock()

	result, err := r.db.Exec(
		`UPDATE recurring_reminders 
		SET label = ?, recurring_type = ?, time = ?, 
		    day_of_week = ?, day_of_month = ?
		WHERE id = ? AND user_id = ? AND active = 1`,
		label,
		string(recurringType),
		timeStr,
		sql.NullInt64{Int64: int64(dayOfWeek), Valid: dayOfWeek >= 0},
		sql.NullInt64{Int64: int64(dayOfMonth), Valid: dayOfMonth > 0},
		id,
		userID,
	)

	if err != nil {
		return false, err
	}

	rowsAffected, err := result.RowsAffected()
	return rowsAffected > 0, err
}

// DeleteRecurringReminder deletes a recurring reminder (sets active to false)
func (r *ReminderRepository) DeleteRecurringReminder(id, userID int64) (bool, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	result, err := r.db.Exec(
		"UPDATE recurring_reminders SET active = 0 WHERE id = ? AND user_id = ? AND active = 1",
		id, userID,
	)
	if err != nil {
		return false, err
	}

	rowsAffected, err := result.RowsAffected()
	return rowsAffected > 0, err
}

// GetDueRecurringReminders gets recurring reminders that are due
func (r *ReminderRepository) GetDueRecurringReminders(now time.Time) ([]RecurringReminder, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	// We need to get recurring reminders where:
	// 1. For daily: the current time matches the reminder time
	// 2. For weekly: the current day of week matches and time matches
	// 3. For monthly: the current day of month matches and time matches
	// 4. The reminder hasn't been triggered today or was triggered long ago

	currentTime := now.Format("15:04")
	currentDayOfWeek := int(now.Weekday())
	currentDayOfMonth := now.Day()

	// Calculate the start of today
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	query := `
	SELECT id, chat_id, user_id, label, created_at, recurring_type, 
		   time, IFNULL(day_of_week, -1), IFNULL(day_of_month, -1), 
		   IFNULL(last_triggered, '1970-01-01 00:00:00'), active
	FROM recurring_reminders
	WHERE active = 1 
	  AND time = ? 
	  AND (
		  (recurring_type = 'daily') OR
		  (recurring_type = 'weekly' AND day_of_week = ?) OR
		  (recurring_type = 'monthly' AND day_of_month = ?)
	  )
	  AND (last_triggered IS NULL OR last_triggered < ?)
	`

	rows, err := r.db.Query(
		query,
		currentTime,
		currentDayOfWeek,
		currentDayOfMonth,
		startOfToday,
	)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []RecurringReminder
	for rows.Next() {
		var r RecurringReminder
		var recurringTypeStr string

		err := rows.Scan(
			&r.ID, &r.ChatID, &r.UserID, &r.Label, &r.CreatedAt,
			&recurringTypeStr, &r.Time, &r.DayOfWeek, &r.DayOfMonth,
			&r.LastTriggered, &r.Active,
		)

		if err != nil {
			return nil, err
		}

		r.RecurringType = RecurringType(recurringTypeStr)
		reminders = append(reminders, r)
	}

	return reminders, nil
}
