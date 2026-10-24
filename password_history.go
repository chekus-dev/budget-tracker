package main

import "golang.org/x/crypto/bcrypt"

// passwordInHistory reports whether password matches any of the user's last
// few previous passwords. The current password is checked separately.
func passwordInHistory(userID int, password string) (bool, error) {
	rows, err := db.Query(
		`SELECT password_hash FROM password_history WHERE user_id = $1 ORDER BY id DESC LIMIT 5`,
		userID,
	)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return false, err
		}
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(password)) == nil {
			return true, nil
		}
	}
	return false, rows.Err()
}
