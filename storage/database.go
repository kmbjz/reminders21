package storage

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
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
	IsTodo       bool
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

// initSchema creates tables if they don't exist and migrates existing tables
func (r *ReminderRepository) initSchema() error {
	// First, create tables if they don't exist
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
    
    CREATE TABLE IF NOT EXISTS recurring_reminders (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        chat_id INTEGER NOT NULL,
        user_id INTEGER NOT NULL,
        label TEXT NOT NULL,
        created_at TIMESTAMP NOT NULL,
        recurring_type TEXT NOT NULL,
        time TEXT NOT NULL,
        day_of_week INTEGER DEFAULT NULL,
        day_of_month INTEGER DEFAULT NULL,
        last_triggered TIMESTAMP DEFAULT NULL,
        active BOOLEAN NOT NULL DEFAULT 1
    );
    
    CREATE INDEX IF NOT EXISTS idx_recurring_user_id ON recurring_reminders(user_id);
    CREATE INDEX IF NOT EXISTS idx_recurring_active ON recurring_reminders(active);
    
    CREATE TABLE IF NOT EXISTS user_preferences (
        user_id INTEGER PRIMARY KEY,
        timezone TEXT NOT NULL DEFAULT 'Europe/Moscow',
        created_at TIMESTAMP NOT NULL,
        updated_at TIMESTAMP NOT NULL
    );
    `
	_, err := r.db.Exec(createTableSQL)
	if err != nil {
		return err
	}

	// Now handle migrations for existing tables
	// Add is_todo column to reminders table if it doesn't exist
	err = r.addColumnIfNotExists("reminders", "is_todo", "INTEGER DEFAULT 0")
	if err != nil {
		return err
	}

	// Add is_todo column to recurring_reminders table if it doesn't exist
	err = r.addColumnIfNotExists("recurring_reminders", "is_todo", "INTEGER DEFAULT 0")
	if err != nil {
		return err
	}

	return nil
}

// addColumnIfNotExists adds a column to a table if it doesn't already exist
func (r *ReminderRepository) addColumnIfNotExists(table, column, definition string) error {
	// Check if the column exists
	var dummy string
	query := fmt.Sprintf("SELECT %s FROM %s LIMIT 1", column, table)

	err := r.db.QueryRow(query).Scan(&dummy)
	if err != nil {
		// Column doesn't exist, add it
		if err == sql.ErrNoRows || strings.Contains(err.Error(), "no such column") {
			r.logger.Printf("Adding column %s to table %s", column, table)
			alterQuery := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)
			_, err := r.db.Exec(alterQuery)
			return err
		}
		return err
	}

	// Column already exists
	return nil
}

// AddReminder adds a new reminder
func (r *ReminderRepository) AddReminder(chatID, userID int64, reminderTime time.Time, label string, isTodo bool) (int64, error) {
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
		"INSERT INTO reminders (chat_id, user_id, reminder_time, label, is_todo) VALUES (?, ?, ?, ?, ?)",
		chatID, userID, reminderTime, label, boolToInt(isTodo),
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

// Helper function to convert bool to int for SQLite
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
        SELECT id, chat_id, user_id, reminder_time, label, notified, is_todo 
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
		var notified, isTodo int
		if err := rows.Scan(&reminder.ID, &reminder.ChatID, &reminder.UserID, &reminder.ReminderTime, &reminder.Label, &notified, &isTodo); err != nil {
			r.logger.Printf("Error scanning reminder row: %v", err)
			continue
		}
		reminder.Notified = notified > 0
		reminder.IsTodo = isTodo > 0
		reminders = append(reminders, reminder)
	}

	return reminders, rows.Err()
}

// GetUserRemindersByPeriod gets reminders for a user within a time period
func (r *ReminderRepository) GetUserRemindersByPeriod(userID int64, start, end time.Time) ([]ReminderItem, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	rows, err := r.db.Query(`
        SELECT id, chat_id, user_id, reminder_time, label, notified, is_todo 
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
		var notified, isTodo int
		if err := rows.Scan(&reminder.ID, &reminder.ChatID, &reminder.UserID, &reminder.ReminderTime, &reminder.Label, &notified, &isTodo); err != nil {
			r.logger.Printf("Error scanning reminder row: %v", err)
			continue
		}
		reminder.Notified = notified > 0
		reminder.IsTodo = isTodo > 0
		reminders = append(reminders, reminder)
	}

	return reminders, rows.Err()
}

// GetDueReminders gets all past-due, unnotified reminders (excluding todos)
func (r *ReminderRepository) GetDueReminders(before time.Time) ([]ReminderItem, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	rows, err := r.db.Query(`
        SELECT id, chat_id, user_id, reminder_time, label, notified, is_todo 
        FROM reminders 
        WHERE reminder_time <= ? AND notified = 0 AND is_todo = 0
        ORDER BY reminder_time`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []ReminderItem
	for rows.Next() {
		var reminder ReminderItem
		var notified, isTodo int
		if err := rows.Scan(&reminder.ID, &reminder.ChatID, &reminder.UserID, &reminder.ReminderTime, &reminder.Label, &notified, &isTodo); err != nil {
			r.logger.Printf("Error scanning reminder row: %v", err)
			continue
		}
		reminder.Notified = notified > 0
		reminder.IsTodo = isTodo > 0
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

// UserPreferences represents user preferences in the database
type UserPreferences struct {
	UserID    int64
	Timezone  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GetUserTimezone gets a user's timezone
func (r *ReminderRepository) GetUserTimezone(userID int64) (string, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	var timezone string
	err := r.db.QueryRow(
		"SELECT timezone FROM user_preferences WHERE user_id = ?",
		userID,
	).Scan(&timezone)

	if err == sql.ErrNoRows {
		// Default to Moscow time if no preference is set
		timezone = "Europe/Moscow"
		// Create a default preference
		_, err = r.db.Exec(
			"INSERT INTO user_preferences (user_id, timezone, created_at, updated_at) VALUES (?, ?, ?, ?)",
			userID, timezone, time.Now(), time.Now(),
		)
		if err != nil {
			return timezone, nil // Return default even if insert fails
		}
		return timezone, nil
	}

	if err != nil {
		return "Europe/Moscow", err // Return default timezone on error
	}

	return timezone, nil
}

// SetUserTimezone sets a user's timezone
func (r *ReminderRepository) SetUserTimezone(userID int64, timezone string) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	// Validate timezone string
	_, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("invalid timezone: %s", timezone)
	}

	now := time.Now()
	_, err = r.db.Exec(
		`INSERT INTO user_preferences (user_id, timezone, created_at, updated_at) 
         VALUES (?, ?, ?, ?)
         ON CONFLICT(user_id) DO UPDATE SET
         timezone = ?, updated_at = ?`,
		userID, timezone, now, now,
		timezone, now,
	)
	return err
}

// GetReminderByID gets a specific reminder by ID
func (r *ReminderRepository) GetReminderByID(id int64) (*ReminderItem, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	var reminder ReminderItem
	var notified int

	err := r.db.QueryRow(`
        SELECT id, chat_id, user_id, reminder_time, label, notified 
        FROM reminders 
        WHERE id = ?`, id).Scan(
		&reminder.ID, &reminder.ChatID, &reminder.UserID,
		&reminder.ReminderTime, &reminder.Label, &notified)

	if err != nil {
		return nil, err
	}

	reminder.Notified = notified > 0
	return &reminder, nil
}

// GetAllActiveChatIDs returns a list of unique chat IDs from all active users
func (r *ReminderRepository) GetAllActiveChatIDs() ([]int64, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	// Get all unique chat IDs from all tables
	query := `
	SELECT DISTINCT chat_id FROM (
		SELECT chat_id FROM reminders WHERE notified = 0
		UNION
		SELECT chat_id FROM recurring_reminders WHERE active = 1
		UNION
		SELECT user_id AS chat_id FROM user_preferences -- Assuming user_id can be used as chat_id for personal chats
	) AS active_chats
	`

	rows, err := r.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chatIDs []int64
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			r.logger.Printf("Error scanning chat ID: %v", err)
			continue
		}
		chatIDs = append(chatIDs, chatID)
	}

	return chatIDs, rows.Err()
}
