package relay

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// generateRandomPassword generates a random 32-character password
func generateRandomPassword() string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const length = 32

	result := make([]byte, length)
	for i := range result {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			// Fallback to simple random if crypto rand fails
			result[i] = charset[i%len(charset)]
			continue
		}
		result[i] = charset[n.Int64()]
	}
	return string(result)
}

// User represents a user in the system
type User struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name"`
}

// AuthStore handles authentication operations
type AuthStore struct {
	db *sql.DB
}

// NewAuthStore creates a new AuthStore
func NewAuthStore(db *sql.DB) *AuthStore {
	return &AuthStore{db: db}
}

// Register creates a new user with email, password, name, and optional username
func (s *AuthStore) Register(email, password, name, username string) (*User, error) {
	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	var usernameVal interface{}
	if username != "" {
		usernameVal = username
	}

	// Insert user
	result, err := s.db.Exec(
		"INSERT INTO users (email, password_hash, name, username) VALUES (?, ?, ?, ?)",
		email, string(hash), name, usernameVal,
	)
	if err != nil {
		return nil, err
	}

	// Get the user ID
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &User{
		ID:       id,
		Email:    email,
		Username: username,
		Name:     name,
	}, nil
}

// Login authenticates a user with email (or username) and password
func (s *AuthStore) Login(identifier, password string) (*User, error) {
	var user User
	var passwordHash string
	var usernameNull sql.NullString

	var err error
	if strings.Contains(identifier, "@") {
		// Query by email
		err = s.db.QueryRow(
			"SELECT id, email, COALESCE(username, ''), password_hash, name FROM users WHERE email = ?",
			identifier,
		).Scan(&user.ID, &user.Email, &usernameNull, &passwordHash, &user.Name)
	} else {
		// Query by username
		err = s.db.QueryRow(
			"SELECT id, email, COALESCE(username, ''), password_hash, name FROM users WHERE username = ?",
			identifier,
		).Scan(&user.ID, &user.Email, &usernameNull, &passwordHash, &user.Name)
	}

	if err == sql.ErrNoRows {
		return nil, errors.New("invalid credentials")
	}
	if err != nil {
		return nil, err
	}

	if usernameNull.Valid {
		user.Username = usernameNull.String
	}

	// Verify password
	err = bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password))
	if err != nil {
		return nil, errors.New("invalid credentials")
	}

	return &user, nil
}

// GetUserByID retrieves a user by ID
func (s *AuthStore) GetUserByID(id int64) (*User, error) {
	var user User
	var usernameNull sql.NullString
	err := s.db.QueryRow(
		"SELECT id, email, COALESCE(username, ''), name FROM users WHERE id = ?",
		id,
	).Scan(&user.ID, &user.Email, &usernameNull, &user.Name)

	if err == sql.ErrNoRows {
		return nil, errors.New("user not found")
	}
	if err != nil {
		return nil, err
	}
	if usernameNull.Valid {
		user.Username = usernameNull.String
	}

	return &user, nil
}

// GetUserByEmail retrieves a user by email
func (s *AuthStore) GetUserByEmail(email string) (*User, error) {
	var user User
	var usernameNull sql.NullString
	err := s.db.QueryRow(
		"SELECT id, email, COALESCE(username, ''), name FROM users WHERE email = ?",
		email,
	).Scan(&user.ID, &user.Email, &usernameNull, &user.Name)

	if err == sql.ErrNoRows {
		return nil, errors.New("user not found")
	}
	if err != nil {
		return nil, err
	}
	if usernameNull.Valid {
		user.Username = usernameNull.String
	}

	return &user, nil
}

// CheckUsernameAvailable checks if a username is available
func (s *AuthStore) CheckUsernameAvailable(username string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", username).Scan(&count)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

// EnsureDevUser creates a dev user if it doesn't exist
func (s *AuthStore) EnsureDevUser() (*User, error) {
	// Check if dev user exists
	user, err := s.GetUserByEmail("dev@local")
	if err == nil {
		return user, nil // Already exists
	}

	// Generate a random password (won't be used in dev mode)
	randomPassword := generateRandomPassword()

	// Create dev user
	return s.Register("dev@local", randomPassword, "Dev User", "")
}
