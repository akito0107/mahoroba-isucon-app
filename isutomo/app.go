package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/newrelic/go-agent"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"github.com/go-redis/redis"
)

type Friend struct {
	ID      int64    `db:"id"`
	Me      string   `db:"me"`
	Friends []string `db:"friends"`
}

type DB struct {
	Host string
	Port string
	User string
	Pass string
	Name string
	Conn *sql.DB
}

var (
	conn    *DB
	a       newrelic.Application
	rclient *redis.Client
)

func (db *DB) initEnvs() error {

	db.Host = os.Getenv("ISUTOMO_DB_HOST")
	db.Port = os.Getenv("ISUTOMO_DB_PORT")
	db.User = os.Getenv("ISUTOMO_DB_USER")
	db.Pass = os.Getenv("ISUTOMO_DB_PASSWORD")
	db.Name = os.Getenv("ISUTOMO_DB_NAME")

	if len(db.Host) == 0 {
		db.Host = "localhost"
	}

	if len(db.Port) == 0 {
		db.Port = "3306"
	}

	if len(db.User) == 0 {
		db.User = "root"
	}

	if len(db.Name) == 0 {
		db.Name = "isutomo"
	}

	return nil
}

func (db *DB) dsn() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local&interpolateParams=true", db.User, db.Pass, db.Host, db.Port, db.Name)
}

func (db *DB) connect() error {
	err := db.initEnvs()

	if err != nil {
		return err
	}

	db.Conn, err = sql.Open("mysql", db.dsn())

	if err != nil {
		return err
	}

	return nil
}

func (db *DB) fetchFriend(user string) (*Friend, error) {

	// friend := new(Friend)

	// stmt, err := db.Conn.Prepare("SELECT * FROM friends WHERE me = ?")
	friends, err := rclient.SMembers(user).Result()
	// err = stmt.QueryRow(user).Scan(&friend.ID, &friend.Me, &friend.Friends)
	if err != nil {
		return nil, err
	}

	return &Friend{Me: user, Friends: friends}, nil
}

func (db *DB) updateFriend(user, friend string) error {
	rclient.SAdd("friends", user, friend).Result()
	return nil
}

func (db *DB) deleteFriend(user, friend string) error {
	rclient.SRem("friends", user, friend).Result()
	return nil
}

func getUserHandler(w http.ResponseWriter, r *http.Request) {
	txn := a.StartTransaction("getUserHandler", nil, nil)
	defer txn.End()

	me := mux.Vars(r)["me"]

	friend, err := conn.fetchFriend(me)
	if err != nil {
		errorResponseWriter(w, http.StatusBadRequest, err)
		return
	}

	friendJSON, err := json.Marshal(struct {
		Friends []string `json:"friends"`
	}{
		Friends: friend.Friends,
	})

	if err != nil {
		errorResponseWriter(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(friendJSON)
}

func postUserHandler(w http.ResponseWriter, r *http.Request) {
	txn := a.StartTransaction("postUser", nil, nil)
	defer txn.End()

	me := mux.Vars(r)["me"]

	friend, err := conn.fetchFriend(me)
	if err != nil {
		errorResponseWriter(w, http.StatusBadRequest, err)
		return
	}

	data := struct {
		User string `json:"user"`
	}{}

	err = JSONUnmarshaler(r.Body, &data)

	if err != nil {
		errorResponseWriter(w, http.StatusBadRequest, err)
		return
	}

	isMember, err := rclient.SIsMember(me, data.User).Result()
	if err != nil {
		errorResponseWriter(w, http.StatusBadRequest, err)
		return
	}

	if isMember {
		errJSON, err := json.Marshal(struct {
			Error string `json:"error"`
		}{
			Error: data.User + " is already your friend.",
		})

		if err != nil {
			errorResponseWriter(w, http.StatusInternalServerError, err)
			return
		}

		w.WriteHeader(http.StatusBadRequest)
		w.Write(errJSON)
		return
	}

	conn.updateFriend(me, data.User)

	friendJSON, err := json.Marshal(struct {
		Friends []string `json:"friends"`
	}{
		Friends: append(friend.Friends, data.User),
	})

	if err != nil {
		errorResponseWriter(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(friendJSON)
}

func deleteUserHandler(w http.ResponseWriter, r *http.Request) {
	txn := a.StartTransaction("deleteUser", nil, nil)
	defer txn.End()

	me := mux.Vars(r)["me"]

	friend, err := conn.fetchFriend(me)
	if err != nil {
		errorResponseWriter(w, http.StatusBadRequest, err)
		return
	}

	friends := friend.Friends

	data := struct {
		User string `json:"user"`
	}{}

	err = JSONUnmarshaler(r.Body, &data)

	if err != nil {
		errorResponseWriter(w, http.StatusBadRequest, err)
		return
	}

	isMember, err := rclient.SIsMember(me, data.User).Result()
	if err != nil {
		errorResponseWriter(w, http.StatusBadRequest, err)
		return
	}

	if isMember {
		friends = remove(friends, data.User)
		conn.deleteFriend(me, data.User)

		friendJSON, err := json.Marshal(struct {
			Friends []string `json:"friends"`
		}{
			Friends: friends,
		})

		if err != nil {
			errorResponseWriter(w, http.StatusInternalServerError, err)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write(friendJSON)
		return
	}

	errJSON, err := json.Marshal(struct {
		Error string `json:"error"`
	}{
		Error: data.User + " is not your friend.",
	})

	if err != nil {
		errorResponseWriter(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	w.Write(errJSON)
	return

}

func remove(array []string, tval string) []string {
	result := []string{}
	for _, v := range array {
		if v != tval {
			result = append(result, v)
		}
	}
	return result
}

func errorResponseWriter(w http.ResponseWriter, status int, err error) {
	w.WriteHeader(status)
	w.Write([]byte(err.Error()))
}

func JSONUnmarshaler(body io.Reader, i interface{}) error {

	bufbody := new(bytes.Buffer)

	length, err := bufbody.ReadFrom(body)

	if err != nil && err != io.EOF {
		return err
	}

	if err = json.Unmarshal(bufbody.Bytes()[:length], &i); err != nil {
		return err
	}

	return nil
}
func initializeHandler(w http.ResponseWriter, r *http.Request) {
	path, err := exec.LookPath("mysql")
	if err != nil {
		errorResponseWriter(w, http.StatusInternalServerError, err)
		return
	}

	exec.Command(path, "-u", "root", "-D", "isutomo", "<", "../../sql/seed_isutomo2.sql").Run()
	if err != nil {
		errorResponseWriter(w, http.StatusInternalServerError, err)
		return
	}

	rows, err := conn.Conn.Query("SELECT * FROM friends")
	defer rows.Close()

	var friends []Friend
	for rows.Next() {
		f := new(Friend)
		var str string
		if err = rows.Scan(&f.ID, &f.Me, &str); err != nil {
			log.Fatal(err)
		}
		f.Friends = strings.Split(str, ",")
		friends = append(friends, *f)
	}

	for _, f := range friends {
		for _, fs := range f.Friends {
			rclient.SAdd(f.Me, fs)
		}
	}
	resultJSON, err := json.Marshal(struct {
		Result []string `json:"result"`
	}{
		Result: []string{"ok"},
	})
	if err != nil {
		errorResponseWriter(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(resultJSON)
	return
}

func NewRouter() *mux.Router {

	router := mux.NewRouter().StrictSlash(true)

	router.Methods(http.MethodGet).Path("/initialize").HandlerFunc(initializeHandler)
	router.Methods(http.MethodGet).Path("/{me}").HandlerFunc(getUserHandler)
	router.Methods(http.MethodPost).Path("/{me}").HandlerFunc(postUserHandler)
	router.Methods(http.MethodDelete).Path("/{me}").HandlerFunc(deleteUserHandler)
	return router
}

func main() {
	cfg := newrelic.NewConfig("ISUCONApp", "31897725152cfdc8c8def14ebbabc1fbe4e8f050")
	var e error
	a, e = newrelic.NewApplication(cfg)
	if nil != e {
		fmt.Println(e)
		os.Exit(1)
	}
	rclient = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	conn = new(DB)
	err := conn.connect()
	if err != nil {
		log.Fatal(err)
	}

	log.Fatalln(http.ListenAndServe(":8081", NewRouter()))
}
