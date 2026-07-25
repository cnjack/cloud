package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cnjack/jcloud/internal/keyrotate"
	"github.com/jackc/pgx/v5"
)

func main() {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		fatal("DATABASE_URL is required")
	}
	oldKey, err := secret("OLD_JCLOUD_MASTER_KEY")
	if err != nil {
		fatal(err.Error())
	}
	newKey, err := secret("NEW_JCLOUD_MASTER_KEY")
	if err != nil {
		fatal(err.Error())
	}
	if oldKey == newKey {
		fatal("old and new master keys must differ")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		fatal("connect to PostgreSQL: " + err.Error())
	}
	defer conn.Close(ctx)
	result, err := keyrotate.Rotate(ctx, conn, oldKey, newKey)
	if err != nil {
		fatal("rotation rolled back: " + err.Error())
	}
	fmt.Printf("rotated %d encrypted rows in one transaction\n", result.TotalRows)
}

func secret(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	file := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if value != "" && file != "" {
		return "", fmt.Errorf("set only one of %s or %s_FILE", name, name)
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read %s_FILE: %w", name, err)
		}
		value = strings.TrimSpace(string(data))
	}
	if value == "" {
		return "", fmt.Errorf("%s or %s_FILE is required", name, name)
	}
	return value, nil
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "jcloud-key-rotate:", message)
	os.Exit(1)
}
