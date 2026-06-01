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
		var externalUserID, name string
		if err := rows.Scan(&externalUserID, &name); err != nil {
			return err
		}
		externalUserID = strings.TrimSpace(externalUserID)
		if externalUserID == "" {
			continue
		}
		labels[externalUserID] = userLabel(externalUserID, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	c.labels = labels
	c.mu.Unlock()
	return nil
}

func (c *UserCache) Set(externalUserID, name string) {
	if c == nil {
		return
	}
	externalUserID = strings.TrimSpace(externalUserID)
	if externalUserID == "" {
		return
	}
	c.mu.Lock()
	c.labels[externalUserID] = userLabel(externalUserID, name)
	c.mu.Unlock()
}

func (c *UserCache) Label(externalUserID string) string {
	externalUserID = strings.TrimSpace(externalUserID)
	if externalUserID == "" {
		return ""
	}
	if c == nil {
		return externalUserID
	}
	c.mu.RLock()
	label, ok := c.labels[externalUserID]
	c.mu.RUnlock()
	if ok && strings.TrimSpace(label) != "" {
		return label
	}
	return externalUserID
}

func userLabel(externalUserID, name string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	return externalUserID
}
