package main

import (
	"context"
	"database/sql"
	"fmt"
	mysql "github.com/go-sql-driver/mysql"
	"os"
	"time"
)

func main() {
	if os.Getenv("METRICSPIRE_DORIS_DISPOSABLE") != "1" {
		panic("explicit disposable lab opt-in required")
	}
	pw := os.Getenv("METRICSPIRE_LAB_PASSWORD")
	if pw == "" {
		panic("lab password required")
	}
	c := mysql.NewConfig()
	c.User = "root"
	c.Net = "tcp"
	c.Addr = "127.0.0.1:19030"
	c.Timeout = 2 * time.Second
	c.ReadTimeout = 3 * time.Second
	c.WriteTimeout = 3 * time.Second
	c.InterpolateParams = true
	for i := 0; i < 60; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c.Passwd = pw
		con, _ := mysql.NewConnector(c)
		db := sql.OpenDB(con)
		err := db.PingContext(ctx)
		if err != nil {
			db.Close()
			c.Passwd = ""
			con, _ = mysql.NewConnector(c)
			db = sql.OpenDB(con)
			err = db.PingContext(ctx)
		}
		cancel()
		if err == nil {
			rows, err := db.Query("SHOW BACKENDS")
			alive := false
			if err == nil {
				cols, _ := rows.Columns()
				for rows.Next() {
					vals := make([]sql.NullString, len(cols))
					dest := make([]any, len(cols))
					for j := range dest {
						dest[j] = &vals[j]
					}
					if rows.Scan(dest...) == nil {
						for j, col := range cols {
							if col == "Alive" && vals[j].String == "true" {
								alive = true
							}
						}
					}
				}
				rows.Close()
			}
			if alive && storageReady(db) {
				if _, err = db.Exec("SET PASSWORD FOR 'root' = PASSWORD(?)", pw); err != nil {
					panic("lab password bootstrap failed")
				}
				db.Close()
				fmt.Println("Doris disposable backend alive; lab root credential installed")
				return
			}
		}
		db.Close()
		time.Sleep(2 * time.Second)
	}
	panic("disposable FE/BE not ready")
}

// Alive can precede the first disk report; require real disposable storage.
func storageReady(db *sql.DB) bool {
	name := fmt.Sprintf("metricspire_readiness_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		return false
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, _ = db.ExecContext(cleanup, "DROP DATABASE IF EXISTS `"+name+"`")
	}()
	table := "`" + name + "`.probe"
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+"(id BIGINT NOT NULL) ENGINE=OLAP DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES('replication_num'='1')"); err != nil {
		return false
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(1)"); err != nil {
		return false
	}
	var count int64
	return db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count) == nil && count == 1
}
