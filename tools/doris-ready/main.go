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
			if alive {
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
