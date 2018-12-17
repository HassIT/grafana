// Copyright 2013 Beego Authors
// Copyright 2014 The Macaron Authors
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package session

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/go-macaron/session"
)

// MysqlStore represents a mysql session store implementation.
type MysqlStore struct {
	c      *sql.DB
	sid    string
	lock   sync.RWMutex
	data   map[interface{}]interface{}
	expiry int64
	dirty  bool
}

// NewMysqlStore creates and returns a mysql session store.
func NewMysqlStore(c *sql.DB, sid string, kv map[interface{}]interface{}, expiry int64) *MysqlStore {
	return &MysqlStore{
		c:      c,
		sid:    sid,
		data:   kv,
		expiry: expiry,
		dirty:  false,
	}
}

// Set sets value to given key in session.
func (s *MysqlStore) Set(key, val interface{}) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.data[key] = val
	s.dirty = true
	return nil
}

// Get gets value by given key in session.
func (s *MysqlStore) Get(key interface{}) interface{} {
	s.lock.RLock()
	defer s.lock.RUnlock()

	return s.data[key]
}

// Delete delete a key from session.
func (s *MysqlStore) Delete(key interface{}) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	delete(s.data, key)
	s.dirty = true
	return nil
}

// ID returns current session ID.
func (s *MysqlStore) ID() string {
	return s.sid
}

// Release releases resource and save data to provider.
func (s *MysqlStore) Release() error {
	newExpiry := time.Now().Unix()
	if !s.dirty && (s.expiry+60) >= newExpiry {
		// log.Printf("session/mysql: skipping update for session %s", s.sid)
		return nil
	}

	data, err := session.EncodeGob(s.data)
	if err != nil {
		return err
	}

	_, err = s.c.Exec("UPDATE session SET data=?, expiry=? WHERE `key`=?",
		data, newExpiry, s.sid)
	s.dirty = false
	s.expiry = newExpiry
	// log.Printf("session/mysql: updated session %s", s.sid)
	return err
}

// Flush deletes all session data.
func (s *MysqlStore) Flush() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.data = make(map[interface{}]interface{})
	s.dirty = true
	return nil
}

// MysqlProvider represents a mysql session provider implementation.
type MysqlProvider struct {
	c      *sql.DB
	expire int64
}

// Init initializes mysql session provider.
// connStr: username:password@protocol(address)/dbname?param=value
func (p *MysqlProvider) Init(expire int64, connStr string) (err error) {
	p.expire = expire

	p.c, err = sql.Open("mysql", connStr)
	p.c.SetConnMaxLifetime(time.Second * time.Duration(sessionConnMaxLifetime))
	if err != nil {
		return err
	}
	return p.c.Ping()
}

// Read returns raw session store by session ID.
func (p *MysqlProvider) Read(sid string) (session.RawStore, error) {
	expiry := time.Now().Unix()
	var data []byte
	err := p.c.QueryRow("SELECT data,expiry FROM session WHERE `key`=?", sid).Scan(&data, &expiry)
	if err == sql.ErrNoRows {
		_, err = p.c.Exec("INSERT INTO session(`key`,data,expiry) VALUES(?,?,?)",
			sid, "", expiry)
	}
	if err != nil {
		return nil, err
	}

	var kv map[interface{}]interface{}
	if len(data) == 0 {
		kv = make(map[interface{}]interface{})
	} else {
		kv, err = session.DecodeGob(data)
		if err != nil {
			return nil, err
		}
	}

	return NewMysqlStore(p.c, sid, kv, expiry), nil
}

// Exist returns true if session with given ID exists.
func (p *MysqlProvider) Exist(sid string) bool {
	exists, err := p.queryExists(sid)

	if err != nil {
		exists, err = p.queryExists(sid)
	}

	if err != nil {
		log.Printf("session/mysql: error checking if session exists: %v", err)
		return false
	}

	return exists
}

func (p *MysqlProvider) queryExists(sid string) (bool, error) {
	var data []byte
	err := p.c.QueryRow("SELECT data FROM session WHERE `key`=?", sid).Scan(&data)

	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	return err != sql.ErrNoRows, nil
}

// Destory deletes a session by session ID.
func (p *MysqlProvider) Destory(sid string) error {
	_, err := p.c.Exec("DELETE FROM session WHERE `key`=?", sid)
	return err
}

// Regenerate regenerates a session store from old session ID to new one.
func (p *MysqlProvider) Regenerate(oldsid, sid string) (_ session.RawStore, err error) {
	if p.Exist(sid) {
		return nil, fmt.Errorf("new sid '%s' already exists", sid)
	}

	if !p.Exist(oldsid) {
		if _, err = p.c.Exec("INSERT INTO session(`key`,data,expiry) VALUES(?,?,?)",
			oldsid, "", time.Now().Unix()); err != nil {
			return nil, err
		}
	}

	if _, err = p.c.Exec("UPDATE session SET `key`=? WHERE `key`=?", sid, oldsid); err != nil {
		return nil, err
	}

	return p.Read(sid)
}

// Count counts and returns number of sessions.
func (p *MysqlProvider) Count() (total int) {
	if err := p.c.QueryRow("SELECT COUNT(*) AS NUM FROM session").Scan(&total); err != nil {
		panic("session/mysql: error counting records: " + err.Error())
	}
	return total
}

// GC calls GC to clean expired sessions.
func (p *MysqlProvider) GC() {
	var err error
	if _, err = p.c.Exec("DELETE FROM session WHERE  expiry + ? <= UNIX_TIMESTAMP(NOW())", p.expire); err != nil {
		_, err = p.c.Exec("DELETE FROM session WHERE  expiry + ? <= UNIX_TIMESTAMP(NOW())", p.expire)
	}

	if err != nil {
		log.Printf("session/mysql: error garbage collecting: %v", err)
	}
}

func init() {
	session.Register("mysql", &MysqlProvider{})
}
