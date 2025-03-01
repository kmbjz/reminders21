package storage

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ReminderRepository handles database operations for reminders
type ReminderRepository struct {
	db     *sql.DB
	lock   sync.Mutex
	logger *log.Logger
}

// ReminderItem represents a reminder in the database
type ReminderItem struct {
	ID           int64
	ChatID       int64
	UserID       int64
	ReminderTime time.Time
	Label        string
	Notified     bool
}

// NewReminderRepository creates a new ReminderRepository
func NewReminderRepository(dbPath string, logger *log.Logger) (*ReminderRepository, error) {
	connStr := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL", dbPath)
	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	repo := &ReminderRepository{
		db:     db,
		logger: logger,
	}

	if err := repo.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	logger.Println("Database initialized successfully")
	return repo, nil
}

// Close closes the database connection
func (r *ReminderRepository) Close() error {
	return r.db.Close()
}

// initSchema creates tables if they don't exist
func (r *ReminderRepository) initSchema() error {
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS reminders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER,
		user_id INTEGER,
		reminder_time DATETIME,
		label TEXT,
		notified INTEGER DEFAULT 0
	);
	
	CREATE INDEX IF NOT EXISTS idx_reminders_time ON reminders(reminder_time);
	CREATE INDEX IF NOT EXISTS idx_reminders_user ON reminders(user_id);
	`
	_, err := r.db.Exec(createTableSQL)
	return err
}

// AddReminder adds a new reminder
func (r *ReminderRepository) AddReminder(chatID, userID int64, reminderTime time.Time, label string) (int64, error) {
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
		"INSERT INTO reminders (chat_id, user_id, reminder_time, label) VALUES (?, ?, ?, ?)",
		chatID, userID, reminderTime, label,
	)
	if err != nil {
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return id, nil
}

// UpdateReminderTime updates the time of a reminder
func (r *ReminderRepository) UpdateReminderTime(id, userID int64, reminderTime time.Time) (bool, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	result, err := r.db.Exec(
		"UPDATE reminders SET reminder_time = ? WHERE id = ? AND user_id = ? AND notified = 0",
		reminderTime, id, userID,
	)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	return rows > 0, err
}

// UpdateReminderLabel updates the label of a reminder
func (r *ReminderRepository) UpdateReminderLabel(id, userID int64, label string) (bool, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	result, err := r.db.Exec(
		"UPDATE reminders SET label = ? WHERE id = ? AND user_id = ? AND notified = 0",
		label, id, userID,
	)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	return rows > 0, err
}

// UpdateReminder updates both time and label of a reminder
func (r *ReminderRepository) UpdateReminder(id, userID int64, reminderTime time.Time, label string) (bool, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	result, err := r.db.Exec(
		"UPDATE reminders SET reminder_time = ?, label = ? WHERE id = ? AND user_id = ? AND notified = 0",
		reminderTime, label, id, userID,
	)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	return rows > 0, err
}

// DeleteReminder deletes a reminder
func (r *ReminderRepository) DeleteReminder(id, userID int64) (bool, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	result, err := r.db.Exec(
		"DELETE FROM reminders WHERE id = ? AND user_id = ? AND notified = 0",
		id, userID,
	)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	return rows > 0, err
}

// GetUserReminders gets all active reminders for a user
func (r *ReminderRepository) GetUserReminders(userID int64) ([]ReminderItem, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	rows, err := r.db.Query(`
		SELECT id, chat_id, user_id, reminder_time, label, notified 
		FROM reminders 
		WHERE user_id = ? AND notified = 0 
		ORDER BY reminder_time`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []ReminderItem
	for rows.Next() {
		var reminder ReminderItem
		var notified int
		if err := rows.Scan(&reminder.ID, &reminder.ChatID, &reminder.UserID, &reminder.ReminderTime, &reminder.Label, &notified); err != nil {
			r.logger.Printf("Error scanning reminder row: %v", err)
			continue
		}
		reminder.Notified = notified > 0
		reminders = append(reminders, reminder)
	}

	return reminders, rows.Err()
}

// GetUserRemindersByPeriod gets reminders for a user within a time period
func (r *ReminderRepository) GetUserRemindersByPeriod(userID int64, start, end time.Time) ([]ReminderItem, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	rows, err := r.db.Query(`
		SELECT id, chat_id, user_id, reminder_time, label, notified 
		FROM reminders 
		WHERE user_id = ? AND notified = 0 AND reminder_time >= ? AND reminder_time < ? 
		ORDER BY reminder_time`, userID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []ReminderItem
	for rows.Next() {
		var reminder ReminderItem
		var notified int
		if err := rows.Scan(&reminder.ID, &reminder.ChatID, &reminder.UserID, &reminder.ReminderTime, &reminder.Label, &notified); err != nil {
			r.logger.Printf("Error scanning reminder row: %v", err)
			continue
		}
		reminder.Notified = notified > 0
		reminders = append(reminders, reminder)
	}

	return reminders, rows.Err()
}

// GetDueReminders gets all past-due, unnotified reminders
func (r *ReminderRepository) GetDueReminders(before time.Time) ([]ReminderItem, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	rows, err := r.db.Query(`
		SELECT id, chat_id, user_id, reminder_time, label, notified 
		FROM reminders 
		WHERE reminder_time <= ? AND notified = 0 
		ORDER BY reminder_time`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []ReminderItem
	for rows.Next() {
		var reminder ReminderItem
		var notified int
		if err := rows.Scan(&reminder.ID, &reminder.ChatID, &reminder.UserID, &reminder.ReminderTime, &reminder.Label, &notified); err != nil {
			r.logger.Printf("Error scanning reminder row: %v", err)
			continue
		}
		reminder.Notified = notified > 0
		reminders = append(reminders, reminder)
	}

	return reminders, rows.Err()
}

// MarkAsNotified marks a reminder as notified
func (r *ReminderRepository) MarkAsNotified(id int64) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	_, err := r.db.Exec("UPDATE reminders SET notified = 1 WHERE id = ?", id)
	return err
}

// MarkMultipleAsNotified marks multiple reminders as notified in one transaction
func (r *ReminderRepository) MarkMultipleAsNotified(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	r.lock.Lock()
	defer r.lock.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare("UPDATE reminders SET notified = 1 WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			return err
		}
	}

	return tx.Commit()
}
