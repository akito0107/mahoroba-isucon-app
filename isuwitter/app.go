package main

import (
	"database/sql"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
    "strconv"
    "runtime"

	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/unrolled/render"
    "github.com/patrickmn/go-cache"
	"net"
	"os/signal"
	"syscall"
	"os/exec"
	"github.com/go-redis/redis"
)

type Tweet struct {
	ID        int
	UserID    int
	Text      string
	CreatedAt time.Time

	UserName string
	HTML     string
	Time     string
}

type Friend struct {
	ID      int64    `db:"id"`
	Me      string   `db:"me"`
	Friends []string `db:"friends"`
}

type User struct {
	ID       int
	Name     string
	Salt     string
	Password string
}

const (
	sessionName     = "isuwitter_session"
	sessionSecret   = "isuwitter"
	perPage         = 50
	isutomoEndpoint = "http://localhost:8081"
)

var (
	re             *render.Render
	store          *sessions.FilesystemStore
	db             *sql.DB
	dbfriend             *sql.DB
	errInvalidUser = errors.New("Invalid User")
	// a              newrelic.Application
    c              = cache.New(3*time.Minute, 5*time.Minute)
	rclient *redis.Client
)

func getuserID(name string) int {
	row := db.QueryRow(`SELECT id FROM users WHERE name = ?`, name)
	user := User{}
	err := row.Scan(&user.ID)
	if err != nil {
		return 0
	}
	return user.ID
}

func getUserName(id int) string {
    key := strconv.Itoa(id)

    name, found := c.Get(key)
    if found {
        return name.(string)
    }

	row := db.QueryRow(`SELECT name FROM users WHERE id = ?`, id)
	user := User{}
	err := row.Scan(&user.Name)
	if err != nil {
		return ""
	}
    c.Set(key, user.Name, cache.DefaultExpiration)
	return user.Name
}

var reg = regexp.MustCompile("#(\\S+)(\\s|$)")

func htmlify(tweet string) string {
	tweet = strings.Replace(tweet, "&", "&amp;", -1)
	tweet = strings.Replace(tweet, "<", "&lt;", -1)
	tweet = strings.Replace(tweet, ">", "&gt;", -1)
	tweet = strings.Replace(tweet, "'", "&apos;", -1)
	tweet = strings.Replace(tweet, "\"", "&quot;", -1)
	tweet = reg.ReplaceAllStringFunc(tweet, func(tag string) string {
		return fmt.Sprintf("<a class=\"hashtag\" href=\"/hashtag/%s\">#%s</a>", tag[1:len(tag)], html.EscapeString(tag[1:len(tag)]))
	})
	return tweet
}

func loadFriends(name string) ([]string, error) {

	//resp, err := http.DefaultClient.Get(pathURIEscape(fmt.Sprintf("%s/%s", isutomoEndpoint, name)))
	//if err != nil {
	//	return nil, err
	//}
	//defer resp.Body.Close()

	//var data struct {
	//	Result []string `json:"friends"`
	//}
	//err = json.NewDecoder(resp.Body).Decode(&data)
	return rclient.SMembers(name).Result()
	// return data.Result, err
}

func initializeHandler(w http.ResponseWriter, r *http.Request) {
	_, err := db.Exec(`DELETE FROM tweets WHERE id > 100000`)
	if err != nil {
		badRequest(w)
		return
	}

	_, err = db.Exec(`DELETE FROM users WHERE id > 1000`)
	if err != nil {
		badRequest(w)
		return
	}

	//resp, err := http.Get(fmt.Sprintf("%s/initialize", isutomoEndpoint))
	//if err != nil {
	//	badRequest(w)
	//	return
	//}
	//defer resp.Body.Close()

	//path, _ := exec.LookPath("mysql")
	//exec.Command(path, "-u", "root", "-D", "isutomo", "<", "../../sql/seed_isutomo2.sql").Run()
	//defer dbfriend.Close()
	//rows, _ := dbfriend.Query("SELECT * FROM friends")
	//var friends []Friend
	//for rows.Next() {
	//	f := &Friend{}
	//	var str string
	//	rows.Scan(&f.ID, &f.Me, &str)
	//	f.Friends = strings.Split(str, ",")

	//	friends = append(friends, *f)
	//}
	//for _, f := range friends {
	//	for _, fs := range f.Friends {
	//		rclient.SAdd(f.Me, fs)
	//	}
	//}

	re.JSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func cacheHandler(w http.ResponseWriter, r *http.Request) {
    rows, _ := db.Query("SELECT id, name FROM users")
    for rows.Next() {
        id   := 0
        name := ""
        rows.Scan(&id, &name)
        c.Set(strconv.Itoa(id), name, cache.DefaultExpiration)
        _, err := db.Exec(`UPDATE users set password=? WHERE id=?`, rot1(name), id)
        if err != nil {}
    }
    re.JSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func rot1(s string) string {
    r := ""
    for _, c := range s {
         r = fmt.Sprintf("%s%c", r, rune((int(c) - int('a') + 1) % 26 + int('a')))
    }
    return r
}

func redInitializeHandler(w http.ResponseWriter, r *http.Request) {
	_, err := db.Exec(`DELETE FROM tweets WHERE id > 100000`)
	if err != nil {
		badRequest(w)
		return
	}

	_, err = db.Exec(`DELETE FROM users WHERE id > 1000`)
	if err != nil {
		badRequest(w)
		return
	}
	rclient.FlushAll()

	path, _ := exec.LookPath("mysql")
	exec.Command(path, "-u", "root", "-D", "isutomo", "<", "../../sql/seed_isutomo2.sql").Run()
	defer dbfriend.Close()
	rows, _ := dbfriend.Query("SELECT * FROM friends")
	var friends []Friend
	for rows.Next() {
		f := &Friend{}
		var str string
		rows.Scan(&f.ID, &f.Me, &str)
		f.Friends = strings.Split(str, ",")

		friends = append(friends, *f)
	}
	for _, f := range friends {
		for _, fs := range f.Friends {
			rclient.SAdd(f.Me, fs)
		}
	}

	re.JSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func topHandler(w http.ResponseWriter, r *http.Request) {

	var name string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		name = getUserName(userID.(int))
	}

	if name == "" {
		flush, _ := session.Values["flush"].(string)
		session := getSession(w, r)
		session.Options = &sessions.Options{MaxAge: -1}
		session.Save(r, w)

		re.HTML(w, http.StatusOK, "index", struct {
			Name  string
			Flush string
		}{
			name,
			flush,
		})
		return
	}

	until := r.URL.Query().Get("until")
	var rows *sql.Rows
	var err error
	if until == "" {
		rows, err = db.Query(`SELECT text, created_at, user_name FROM tweets ORDER BY created_at DESC LIMIT 131`)
	} else {
		rows, err = db.Query(`SELECT text, created_at, user_name FROM tweets WHERE created_at < ? ORDER BY created_at DESC LIMIT 131`, until)
	}

	if err != nil {
		log.Println(err)
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		return
	}
	defer rows.Close()

	result, err := loadFriends(name)
	if err != nil {
		log.Println(err)
		badRequest(w)
		return
	}

	tweets := make([]*Tweet, 0)
	for rows.Next() {
		t := Tweet{}
		err := rows.Scan(&t.Text, &t.CreatedAt, &t.UserName)
		if err != nil && err != sql.ErrNoRows {
			log.Println(err)
			badRequest(w)
			return
		}
		// t.HTML = htmlify(t.Text)
		t.HTML = t.Text
		t.Time = t.CreatedAt.Format("2006-01-02 15:04:05")

//		t.UserName = getUserName(t.UserID)
//		if t.UserName == "" {
//			badRequest(w)
//			return
//		}

		for _, x := range result {
			if x == t.UserName {
				tweets = append(tweets, &t)
				break
			}
		}
		if len(tweets) == perPage {
			break
		}
	}

	add := r.URL.Query().Get("append")
	if add != "" {
		re.HTML(w, http.StatusOK, "_tweets", struct {
			Tweets []*Tweet
		}{
			tweets,
		})
		return
	}

	re.HTML(w, http.StatusOK, "index", struct {
		Name   string
		Tweets []*Tweet
	}{
		name, tweets,
	})
}

func tweetPostHandler(w http.ResponseWriter, r *http.Request) {

	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
    u := ""
	if ok {
        u = getUserName(userID.(int))
		if u == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	} else {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	text := r.FormValue("text")
	if text == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	text = htmlify(text)

	_, err := db.Exec(`INSERT INTO tweets (user_id, text, created_at, user_name) VALUES (?, ?, NOW(), ?)`, userID, text, u)
	if err != nil {
		badRequest(w)
		return
	}

	http.Redirect(w, r, "/", http.StatusFound)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {

	name := r.FormValue("name")
	//row := db.QueryRow(`SELECT * FROM users WHERE name = ?`, name)
	row := db.QueryRow(`SELECT id, password FROM users WHERE name = ?`, name)
	user := User{}
    err := row.Scan(&user.ID, &user.Password)
	if err != nil && err != sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
//	if err == sql.ErrNoRows || user.Password != fmt.Sprintf("%x", sha1.Sum([]byte(user.Salt+r.FormValue("password")))) {
	if err == sql.ErrNoRows || user.Password != r.FormValue("password") {
		session := getSession(w, r)
		session.Values["flush"] = "ログインエラー"
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	session := getSession(w, r)
	session.Values["user_id"] = user.ID
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {

	session := getSession(w, r)
	session.Options = &sessions.Options{MaxAge: -1}
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func followHandler(w http.ResponseWriter, r *http.Request) {

	var userName string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		u := getUserName(userID.(int))
		if u == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		userName = u
	} else {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	fre := r.FormValue("user")
	isMember, _ := rclient.SIsMember(userName, fre).Result()
	if isMember {
		badRequest(w)
		return
	}
	rclient.SAdd(userName, fre).Result()

	http.Redirect(w, r, "/", http.StatusFound)
}

func unfollowHandler(w http.ResponseWriter, r *http.Request) {

	var userName string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		u := getUserName(userID.(int))
		if u == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		userName = u
	} else {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	fre := r.FormValue("user")
	isMember, _ := rclient.SIsMember(userName, fre).Result()
	if !isMember {
		badRequest(w)
		return
	}
	rclient.SRem(userName, fre).Result()

	http.Redirect(w, r, "/", http.StatusFound)
}

func getSession(w http.ResponseWriter, r *http.Request) *sessions.Session {
	session, _ := store.Get(r, sessionName)

	return session
}

func pathURIEscape(s string) string {
	return (&url.URL{Path: s}).String()
}

func badRequest(w http.ResponseWriter) {
	code := http.StatusBadRequest
	http.Error(w, http.StatusText(code), code)
}

func userHandler(w http.ResponseWriter, r *http.Request) {

	var name string
	session := getSession(w, r)
	sessionUID, ok := session.Values["user_id"]
	if ok {
		name = getUserName(sessionUID.(int))
	} else {
		name = ""
	}

	user := mux.Vars(r)["user"]
	mypage := user == name

//	userID := getuserID(user)
//	if userID == 0 {
//		http.NotFound(w, r)
//		return
//	}
    userID := session.Values["user_id"]

	isFriend := false
	if name != "" {
		result, err := loadFriends(name)
		if err != nil {
			badRequest(w)
			return
		}

		for _, x := range result {
			if x == user {
				isFriend = true
				break
			}
		}
	}

	until := r.URL.Query().Get("until")
	var rows *sql.Rows
	var err error
	if until == "" {
		rows, err = db.Query(`SELECT text, created_at FROM tweets WHERE user_id = ? ORDER BY created_at DESC LIMIT 131`, userID)
	} else {
		rows, err = db.Query(`SELECT text, created_at FROM tweets WHERE user_id = ? AND created_at < ? ORDER BY created_at DESC LIMIT 131`, userID, until)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		return
	}
	defer rows.Close()

	tweets := make([]*Tweet, 0)
	for rows.Next() {
		t := Tweet{}
		err := rows.Scan(&t.Text, &t.CreatedAt)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			return
		}
		// t.HTML = htmlify(t.Text)
		t.HTML = t.Text
		t.Time = t.CreatedAt.Format("2006-01-02 15:04:05")
		t.UserName = user
		tweets = append(tweets, &t)

		if len(tweets) == perPage {
			break
		}
	}

	add := r.URL.Query().Get("append")
	if add != "" {
		re.HTML(w, http.StatusOK, "_tweets", struct {
			Tweets []*Tweet
		}{
			tweets,
		})
		return
	}

	re.HTML(w, http.StatusOK, "user", struct {
		Name     string
		User     string
		Tweets   []*Tweet
		IsFriend bool
		Mypage   bool
	}{
		name, user, tweets, isFriend, mypage,
	})
}

func searchHandler(w http.ResponseWriter, r *http.Request) {

	var name string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		name = getUserName(userID.(int))
	} else {
		name = ""
	}

	query := r.URL.Query().Get("q")
	if mux.Vars(r)["tag"] != "" {
		query = "#" + mux.Vars(r)["tag"]
	}

	until := r.URL.Query().Get("until")
	var rows *sql.Rows
	var err error
	if until == "" {
		rows, err = db.Query(`SELECT text, created_at, user_name FROM tweets ORDER BY created_at DESC LIMIT 131`)
	} else {
		rows, err = db.Query(`SELECT text, created_at, user_name FROM tweets WHERE created_at < ? ORDER BY created_at DESC LIMIT 131`, until)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		return
	}
	defer rows.Close()

	tweets := make([]*Tweet, 0)
	for rows.Next() {
		t := Tweet{}
		err := rows.Scan(&t.Text, &t.CreatedAt, &t.UserName)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			return
		}
		// t.HTML = htmlify(t.Text)
		t.HTML = t.Text
		t.Time = t.CreatedAt.Format("2006-01-02 15:04:05")
//		t.UserName = getUserName(t.UserID)
//		if t.UserName == "" {
//			badRequest(w)
//			return
//		}
		if strings.Index(t.HTML, query) != -1 {
			tweets = append(tweets, &t)
		}

		if len(tweets) == perPage {
			break
		}
	}

	add := r.URL.Query().Get("append")
	if add != "" {
		re.HTML(w, http.StatusOK, "_tweets", struct {
			Tweets []*Tweet
		}{
			tweets,
		})
		return
	}

	re.HTML(w, http.StatusOK, "search", struct {
		Name   string
		Tweets []*Tweet
		Query  string
	}{
		name, tweets, query,
	})
}

func js(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Write(fileRead("./public/js/script.js"))
}

func css(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "text/css")
	w.Write(fileRead("./public/css/style.css"))
}

func fileRead(fp string) []byte {
	fs, err := os.Open(fp)

	if err != nil {
		return nil
	}

	defer fs.Close()

	l, err := fs.Stat()

	if err != nil {
		return nil
	}

	buf := make([]byte, l.Size())

	_, err = fs.Read(buf)

	if err != nil {
		return nil
	}

	return buf
}

func main() {

    runtime.GOMAXPROCS(6)
	host := os.Getenv("ISUWITTER_DB_HOST")
	if host == "" {
		host = "localhost"
	}
	port := os.Getenv("ISUWITTER_DB_PORT")
	if port == "" {
		port = "3306"
	}
	user := os.Getenv("ISUWITTER_DB_USER")
	if user == "" {
		user = "root"
	}
	password := os.Getenv("ISUWITTER_DB_PASSWORD")
	dbname := os.Getenv("ISUWITTER_DB_NAME")
	if dbname == "" {
		dbname = "isuwitter"
	}

	var err error
	db, err = sql.Open("mysql", fmt.Sprintf(
		"%s:%s@unix(/var/lib/mysql/mysql.sock)/%s?charset=utf8mb4&loc=Local&parseTime=true",
		user, password, dbname,
	))
	db.SetMaxIdleConns(150)

	dbfriend, err = sql.Open("mysql", fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&loc=Local&parseTime=true",
		user, password, host, port, "isutomo",
	))
	if err != nil {
		log.Fatalf("Failed to connect to DB: %s.", err.Error())
	}

	store = sessions.NewFilesystemStore("", []byte(sessionSecret))

	re = render.New(render.Options{
		Directory: "views",
		Funcs: []template.FuncMap{
			{
				"raw": func(text string) template.HTML {
					return template.HTML(text)
				},
				"add": func(a, b int) int { return a + b },
			},
		},
	})
	rclient = redis.NewClient(&redis.Options{
		Network: "unix",
		Addr:     "/tmp/redis.sock",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	r := mux.NewRouter()
	r.HandleFunc("/initialize", initializeHandler).Methods("GET")
    r.HandleFunc("/redis", redInitializeHandler).Methods("GET")

    g := r.PathPrefix("/cacheinit").Subrouter()
    g.Methods("GET").HandlerFunc(cacheHandler)

	l := r.PathPrefix("/login").Subrouter()
	l.Methods("POST").HandlerFunc(loginHandler)
	r.HandleFunc("/logout", logoutHandler)

	//r.PathPrefix("/css/style.css").HandlerFunc(css)
	//r.PathPrefix("/js/script.js").HandlerFunc(js)

	s := r.PathPrefix("/search").Subrouter()
	s.Methods("GET").HandlerFunc(searchHandler)
	t := r.PathPrefix("/hashtag/{tag}").Subrouter()
	t.Methods("GET").HandlerFunc(searchHandler)

	n := r.PathPrefix("/unfollow").Subrouter()
	n.Methods("POST").HandlerFunc(unfollowHandler)
	f := r.PathPrefix("/follow").Subrouter()
	f.Methods("POST").HandlerFunc(followHandler)

	u := r.PathPrefix("/{user}").Subrouter()
	u.Methods("GET").HandlerFunc(userHandler)

	i := r.PathPrefix("/").Subrouter()
	i.Methods("GET").HandlerFunc(topHandler)
	i.Methods("POST").HandlerFunc(tweetPostHandler)

	listener, err := net.Listen("unix", "/tmp/go.sock")
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			log.Printf("error: %v", err)
		}
	}()
	shutdown(listener)

	log.Fatal(http.Serve(listener, r))
}

func shutdown(listener net.Listener) {
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		if err := listener.Close(); err != nil {
			log.Printf("error: %v", err)
		}
		os.Exit(1)
	}()
}
