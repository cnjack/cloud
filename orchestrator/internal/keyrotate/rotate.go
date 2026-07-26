// Package keyrotate implements the offline JCLOUD_MASTER_KEY rotation. It is
// intentionally separate from the serving process so two keys are never
// accepted during normal operation.
package keyrotate

import (
	"context"
	"fmt"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/jackc/pgx/v5"
)

type encryptedColumn struct {
	table  string
	key    string
	column string
}

var encryptedColumns = []encryptedColumn{
	{table: "user_identities", key: "id", column: "access_token_enc"},
	{table: "user_identities", key: "id", column: "refresh_token_enc"},
	{table: "model_providers", key: "id", column: "api_key_enc"},
	{table: "model_providers", key: "id", column: "headers_enc"},
	{table: "model_configs", key: "id", column: "api_key_enc"},
	{table: "model_configs", key: "id", column: "headers_enc"},
	{table: "provider_configs", key: "provider", column: "client_secret_enc"},
	{table: "provider_configs", key: "provider", column: "app_private_key_enc"},
	{table: "provider_configs", key: "provider", column: "webhook_secret_enc"},
	{table: "plugin_installations", key: "id", column: "access_token_enc"},
	{table: "plugin_installations", key: "id", column: "refresh_token_enc"},
	{table: "webhook_bindings", key: "service_id", column: "secret_enc"},
}

type Result struct {
	RowsByColumn map[string]int
	TotalRows    int
}

// Rotate locks and re-encrypts every server-owned secret in one transaction.
// A single undecryptable value aborts the transaction.
func Rotate(ctx context.Context, conn *pgx.Conn, oldKey, newKey string) (Result, error) {
	oldCipher, err := auth.NewCipher(oldKey)
	if err != nil {
		return Result{}, fmt.Errorf("old master key: %w", err)
	}
	newCipher, err := auth.NewCipher(newKey)
	if err != nil {
		return Result{}, fmt.Errorf("new master key: %w", err)
	}
	if oldCipher == nil || newCipher == nil {
		return Result{}, fmt.Errorf("old and new master keys are required")
	}

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result := Result{RowsByColumn: map[string]int{}}
	for _, target := range encryptedColumns {
		count, err := rotateColumn(ctx, tx, oldCipher, newCipher, target)
		if err != nil {
			return Result{}, err
		}
		name := target.table + "." + target.column
		result.RowsByColumn[name] = count
		result.TotalRows += count
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return result, nil
}

func rotateColumn(ctx context.Context, tx pgx.Tx, oldCipher, newCipher *auth.Cipher, target encryptedColumn) (int, error) {
	query := fmt.Sprintf(
		"SELECT %s, %s FROM %s WHERE %s IS NOT NULL AND octet_length(%s) > 0 FOR UPDATE",
		target.key, target.column, target.table, target.column, target.column,
	)
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("read %s.%s: %w", target.table, target.column, err)
	}
	type value struct {
		id   string
		blob []byte
	}
	var values []value
	for rows.Next() {
		var item value
		if err := rows.Scan(&item.id, &item.blob); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan %s.%s: %w", target.table, target.column, err)
		}
		values = append(values, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate %s.%s: %w", target.table, target.column, err)
	}
	rows.Close()

	update := fmt.Sprintf("UPDATE %s SET %s=$1 WHERE %s=$2", target.table, target.column, target.key)
	for _, item := range values {
		rotated, err := reencrypt(oldCipher, newCipher, item.blob)
		if err != nil {
			return 0, fmt.Errorf("decrypt %s.%s row %s: %w", target.table, target.column, item.id, err)
		}
		tag, err := tx.Exec(ctx, update, rotated, item.id)
		if err != nil {
			return 0, fmt.Errorf("update %s.%s row %s: %w", target.table, target.column, item.id, err)
		}
		if tag.RowsAffected() != 1 {
			return 0, fmt.Errorf("update %s.%s row %s affected %d rows", target.table, target.column, item.id, tag.RowsAffected())
		}
	}
	return len(values), nil
}

func reencrypt(oldCipher, newCipher *auth.Cipher, blob []byte) ([]byte, error) {
	plaintext, err := oldCipher.Decrypt(blob)
	if err != nil {
		return nil, err
	}
	return newCipher.Encrypt(plaintext)
}
