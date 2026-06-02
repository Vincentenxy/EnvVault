package postgres

import (
	"context"
	"database/sql"
	"strings"
	"sync"
)

type UserCache struct {
	mu     sync.RWMutex
	labels map[string]string
}

func NewUserCache() *UserCache {
	return &UserCache{labels: make(map[string]string)}
}

func (c *UserCache) Load(ctx context.Context, db *sql.DB) error {
	if c == nil || db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `
select external_user_id, name
from users
`)
	if err != nil {
		return err
	}
	defer rows.Close()

	labels := make(map[string]string)
	for rows.Next() {
		var externalUserId, name string
		if err := rows.Scan(&externalUserId, &name); err != nil {
			return err
		}
		externalUserId = strings.TrimSpace(externalUserId)
		if externalUserId == "" {
			continue
		}
		labels[externalUserId] = userLabel(externalUserId, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	c.labels = labels
	c.mu.Unlock()
	return nil
}

func (c *UserCache) Set(externalUserId, name string) {
	if c == nil {
		return
	}
	externalUserId = strings.TrimSpace(externalUserId)
	if externalUserId == "" {
		return
	}
	c.mu.Lock()
	c.labels[externalUserId] = userLabel(externalUserId, name)
	c.mu.Unlock()
}

func (c *UserCache) Label(externalUserId string) string {
	externalUserId = strings.TrimSpace(externalUserId)
	if externalUserId == "" {
		return ""
	}
	if c == nil {
		return externalUserId
	}
	c.mu.RLock()
	label, ok := c.labels[externalUserId]
	c.mu.RUnlock()
	if ok && strings.TrimSpace(label) != "" {
		return label
	}
	return externalUserId
}

func userLabel(externalUserId, name string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	return externalUserId
}
