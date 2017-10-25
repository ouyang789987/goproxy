package utils

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Checker struct {
	data       ConcurrentMap
	blockedMap ConcurrentMap
	directMap  ConcurrentMap
	interval   int64
	timeout    int
}
type CheckerItem struct {
	IsHTTPS      bool
	Method       string
	URL          string
	Domain       string
	Host         string
	Data         []byte
	SuccessCount uint
	FailCount    uint
}

//NewChecker args:
//timeout : tcp timeout milliseconds ,connect to host
//interval: recheck domain interval seconds
func NewChecker(timeout int, interval int64, blockedFile, directFile string) Checker {
	ch := Checker{
		data:     NewConcurrentMap(),
		interval: interval,
		timeout:  timeout,
	}
	ch.blockedMap = ch.loadMap(blockedFile)
	ch.directMap = ch.loadMap(directFile)
	if !ch.blockedMap.IsEmpty() {
		log.Printf("blocked file loaded , domains : %d", ch.blockedMap.Count())
	}
	if !ch.directMap.IsEmpty() {
		log.Printf("direct file loaded , domains : %d", ch.directMap.Count())
	}
	ch.start()
	return ch
}

func (c *Checker) loadMap(f string) (dataMap ConcurrentMap) {
	dataMap = NewConcurrentMap()
	if PathExists(f) {
		_contents, err := ioutil.ReadFile(f)
		if err != nil {
			log.Printf("load file err:%s", err)
			return
		}
		for _, line := range strings.Split(string(_contents), "\n") {
			line = strings.Trim(line, "\r \t")
			if line != "" {
				dataMap.Set(line, true)
			}
		}
	}
	return
}
func (c *Checker) start() {
	go func() {
		for {
			for _, v := range c.data.Items() {
				go func(item CheckerItem) {
					if c.isNeedCheck(item) {
						//log.Printf("check %s", item.Domain)
						var conn net.Conn
						var err error
						if item.IsHTTPS {
							conn, err = ConnectHost(item.Host, c.timeout)
							if err == nil {
								conn.SetDeadline(time.Now().Add(time.Millisecond))
								conn.Close()
							}
						} else {
							err = HTTPGet(item.URL, c.timeout)
						}
						if err != nil {
							item.FailCount = item.FailCount + 1
						} else {
							item.SuccessCount = item.SuccessCount + 1
						}
						c.data.Set(item.Host, item)
					}
				}(v.(CheckerItem))
			}
			time.Sleep(time.Second * time.Duration(c.interval))
		}
	}()
}
func (c *Checker) isNeedCheck(item CheckerItem) bool {
	var minCount uint = 5
	if (item.SuccessCount >= minCount && item.SuccessCount > item.FailCount) ||
		(item.FailCount >= minCount && item.SuccessCount > item.FailCount) ||
		c.domainIsInMap(item.Host, false) ||
		c.domainIsInMap(item.Host, true) {
		return false
	}
	return true
}
func (c *Checker) IsBlocked(address string) (blocked bool, failN, successN uint) {
	if c.domainIsInMap(address, true) {
		//log.Printf("%s in blocked ? true", address)
		return true, 0, 0
	}
	if c.domainIsInMap(address, false) {
		//log.Printf("%s in direct ? true", address)
		return false, 0, 0
	}

	_item, ok := c.data.Get(address)
	if !ok {
		//log.Printf("%s not in map, blocked true", address)
		return true, 0, 0
	}
	item := _item.(CheckerItem)

	return item.FailCount >= item.SuccessCount, item.FailCount, item.SuccessCount
}
func (c *Checker) domainIsInMap(address string, blockedMap bool) bool {
	u, err := url.Parse("http://" + address)
	if err != nil {
		log.Printf("blocked check , url parse err:%s", err)
		return true
	}
	domainSlice := strings.Split(u.Hostname(), ".")
	if len(domainSlice) > 1 {
		subSlice := domainSlice[:len(domainSlice)-1]
		topDomain := strings.Join(domainSlice[len(domainSlice)-1:], ".")
		checkDomain := topDomain
		for i := len(subSlice) - 1; i >= 0; i-- {
			checkDomain = subSlice[i] + "." + checkDomain
			if !blockedMap && c.directMap.Has(checkDomain) {
				return true
			}
			if blockedMap && c.blockedMap.Has(checkDomain) {
				return true
			}
		}
	}
	return false
}
func (c *Checker) Add(address string, isHTTPS bool, method, URL string, data []byte) {
	if c.domainIsInMap(address, false) || c.domainIsInMap(address, true) {
		return
	}
	if !isHTTPS && strings.ToLower(method) != "get" {
		return
	}
	var item CheckerItem
	u := strings.Split(address, ":")
	item = CheckerItem{
		URL:     URL,
		Domain:  u[0],
		Host:    address,
		Data:    data,
		IsHTTPS: isHTTPS,
		Method:  method,
	}
	c.data.SetIfAbsent(item.Host, item)
}

type BasicAuth struct {
	data ConcurrentMap
}

func NewBasicAuth() BasicAuth {
	return BasicAuth{
		data: NewConcurrentMap(),
	}
}
func (ba *BasicAuth) AddFromFile(file string) (n int, err error) {
	_content, err := ioutil.ReadFile(file)
	if err != nil {
		return
	}
	userpassArr := strings.Split(strings.Replace(string(_content), "\r", "", -1), "\n")
	for _, userpass := range userpassArr {
		if strings.HasPrefix("#", userpass) {
			continue
		}
		u := strings.Split(strings.Trim(userpass, " "), ":")
		if len(u) == 2 {
			ba.data.Set(u[0], u[1])
			n++
		}
	}
	return
}

func (ba *BasicAuth) Add(userpassArr []string) (n int) {
	for _, userpass := range userpassArr {
		u := strings.Split(userpass, ":")
		if len(u) == 2 {
			ba.data.Set(u[0], u[1])
			n++
		}
	}
	return
}
func (ba *BasicAuth) CheckUserPass(user, pass string) (ok bool) {
	if p, _ok := ba.data.Get(user); _ok {
		return p.(string) == pass
	}
	return
}
func (ba *BasicAuth) Check(userpass string) (ok bool) {
	u := strings.Split(strings.Trim(userpass, " "), ":")
	if len(u) == 2 {
		if p, _ok := ba.data.Get(u[0]); _ok {
			return p.(string) == u[1]
		}
	}
	return
}
func (ba *BasicAuth) Total() (n int) {
	n = ba.data.Count()
	return
}

type HTTPRequest struct {
	HeadBuf     []byte
	conn        *net.Conn
	Host        string
	Method      string
	URL         string
	hostOrURL   string
	isBasicAuth bool
	basicAuth   *BasicAuth
}

func NewHTTPRequest(inConn *net.Conn, bufSize int, isBasicAuth bool, basicAuth *BasicAuth) (req HTTPRequest, err error) {
	buf := make([]byte, bufSize)
	len := 0
	req = HTTPRequest{
		conn: inConn,
	}
	len, err = (*inConn).Read(buf[:])
	if err != nil {
		if err != io.EOF {
			err = fmt.Errorf("http decoder read err:%s", err)
		}
		CloseConn(inConn)
		return
	}
	req.HeadBuf = buf[:len]
	index := bytes.IndexByte(req.HeadBuf, '\n')
	if index == -1 {
		err = fmt.Errorf("http decoder data line err:%s", SubStr(string(req.HeadBuf), 0, 50))
		CloseConn(inConn)
		return
	}
	fmt.Sscanf(string(req.HeadBuf[:index]), "%s%s", &req.Method, &req.hostOrURL)
	if req.Method == "" || req.hostOrURL == "" {
		err = fmt.Errorf("http decoder data err:%s", SubStr(string(req.HeadBuf), 0, 50))
		CloseConn(inConn)
		return
	}
	req.Method = strings.ToUpper(req.Method)
	req.isBasicAuth = isBasicAuth
	req.basicAuth = basicAuth
	log.Printf("%s:%s", req.Method, req.hostOrURL)

	if req.IsHTTPS() {
		err = req.HTTPS()
	} else {
		err = req.HTTP()
	}
	return
}
func (req *HTTPRequest) HTTP() (err error) {
	if req.isBasicAuth {
		err = req.BasicAuth()
		if err != nil {
			return
		}
	}
	req.URL, err = req.getHTTPURL()
	if err == nil {
		u, _ := url.Parse(req.URL)
		req.Host = u.Host
		req.addPortIfNot()
	}
	return
}
func (req *HTTPRequest) HTTPS() (err error) {
	req.Host = req.hostOrURL
	req.addPortIfNot()
	//_, err = fmt.Fprint(*req.conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	return
}
func (req *HTTPRequest) HTTPSReply() (err error) {
	_, err = fmt.Fprint(*req.conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	return
}
func (req *HTTPRequest) IsHTTPS() bool {
	return req.Method == "CONNECT"
}

func (req *HTTPRequest) BasicAuth() (err error) {

	//log.Printf("request :%s", string(b[:n]))
	authorization, err := req.getHeader("Authorization")
	if err != nil {
		fmt.Fprint((*req.conn), "HTTP/1.1 401 Unauthorized\r\nWWW-Authenticate: Basic realm=\"\"\r\n\r\nUnauthorized")
		CloseConn(req.conn)
		return
	}
	//log.Printf("Authorization:%s", authorization)
	basic := strings.Fields(authorization)
	if len(basic) != 2 {
		err = fmt.Errorf("authorization data error,ERR:%s", authorization)
		CloseConn(req.conn)
		return
	}
	user, err := base64.StdEncoding.DecodeString(basic[1])
	if err != nil {
		err = fmt.Errorf("authorization data parse error,ERR:%s", err)
		CloseConn(req.conn)
		return
	}
	authOk := (*req.basicAuth).Check(string(user))
	//log.Printf("auth %s,%v", string(user), authOk)
	if !authOk {
		fmt.Fprint((*req.conn), "HTTP/1.1 401 Unauthorized\r\n\r\nUnauthorized")
		CloseConn(req.conn)
		err = fmt.Errorf("basic auth fail")
		return
	}
	return
}
func (req *HTTPRequest) getHTTPURL() (URL string, err error) {
	if !strings.HasPrefix(req.hostOrURL, "/") {
		return req.hostOrURL, nil
	}
	_host, err := req.getHeader("host")
	if err != nil {
		return
	}
	URL = fmt.Sprintf("http://%s%s", _host, req.hostOrURL)
	return
}
func (req *HTTPRequest) getHeader(key string) (val string, err error) {
	key = strings.ToUpper(key)
	lines := strings.Split(string(req.HeadBuf), "\r\n")
	for _, line := range lines {
		line := strings.SplitN(strings.Trim(line, "\r\n "), ":", 2)
		if len(line) == 2 {
			k := strings.ToUpper(strings.Trim(line[0], " "))
			v := strings.Trim(line[1], " ")
			if key == k {
				val = v
				return
			}
		}
	}
	err = fmt.Errorf("can not find HOST header")
	return
}

func (req *HTTPRequest) addPortIfNot() (newHost string) {
	//newHost = req.Host
	port := "80"
	if req.IsHTTPS() {
		port = "443"
	}
	if (!strings.HasPrefix(req.Host, "[") && strings.Index(req.Host, ":") == -1) || (strings.HasPrefix(req.Host, "[") && strings.HasSuffix(req.Host, "]")) {
		//newHost = req.Host + ":" + port
		//req.headBuf = []byte(strings.Replace(string(req.headBuf), req.Host, newHost, 1))
		req.Host = req.Host + ":" + port
	}
	return
}

type OutPool struct {
	Pool      ConnPool
	dur       int
	typ       string
	certBytes []byte
	keyBytes  []byte
	kcpMethod string
	kcpKey    string
	address   string
	timeout   int
}

func NewOutPool(dur int, typ, kcpMethod, kcpKey string, certBytes, keyBytes []byte, address string, timeout int, InitialCap int, MaxCap int) (op OutPool) {
	op = OutPool{
		dur:       dur,
		typ:       typ,
		certBytes: certBytes,
		keyBytes:  keyBytes,
		kcpMethod: kcpMethod,
		kcpKey:    kcpKey,
		address:   address,
		timeout:   timeout,
	}
	var err error
	op.Pool, err = NewConnPool(poolConfig{
		IsActive: func(conn interface{}) bool { return true },
		Release: func(conn interface{}) {
			if conn != nil {
				conn.(net.Conn).SetDeadline(time.Now().Add(time.Millisecond))
				conn.(net.Conn).Close()
				// log.Println("conn released")
			}
		},
		InitialCap: InitialCap,
		MaxCap:     MaxCap,
		Factory: func() (conn interface{}, err error) {
			conn, err = op.getConn()
			return
		},
	})
	if err != nil {
		log.Fatalf("init conn pool fail ,%s", err)
	} else {
		if InitialCap > 0 {
			log.Printf("init conn pool success")
			op.initPoolDeamon()
		} else {
			log.Printf("conn pool closed")
		}
	}
	return
}
func (op *OutPool) getConn() (conn interface{}, err error) {
	if op.typ == "tls" {
		var _conn tls.Conn
		_conn, err = TlsConnectHost(op.address, op.timeout, op.certBytes, op.keyBytes)
		if err == nil {
			conn = net.Conn(&_conn)
		}
	} else if op.typ == "kcp" {
		conn, err = ConnectKCPHost(op.address, op.kcpMethod, op.kcpKey)
	} else {
		conn, err = ConnectHost(op.address, op.timeout)
	}
	return
}

func (op *OutPool) initPoolDeamon() {
	go func() {
		if op.dur <= 0 {
			return
		}
		log.Printf("pool deamon started")
		for {
			time.Sleep(time.Second * time.Duration(op.dur))
			conn, err := op.getConn()
			if err != nil {
				log.Printf("pool deamon err %s , release pool", err)
				op.Pool.ReleaseAll()
			} else {
				conn.(net.Conn).SetDeadline(time.Now().Add(time.Millisecond))
				conn.(net.Conn).Close()
			}
		}
	}()
}

type HeartbeatData struct {
	Data  []byte
	N     int
	Error error
}
type HeartbeatReadWriter struct {
	conn *net.Conn
	// rchn       chan HeartbeatData
	l          *sync.Mutex
	dur        int
	errHandler func(err error, hb *HeartbeatReadWriter)
	once       *sync.Once
	datachn    chan byte
	// rbuf       bytes.Buffer
	// signal     chan bool
	rerrchn chan error
}

func NewHeartbeatReadWriter(conn *net.Conn, dur int, fn func(err error, hb *HeartbeatReadWriter)) (hrw HeartbeatReadWriter) {
	hrw = HeartbeatReadWriter{
		conn: conn,
		l:    &sync.Mutex{},
		dur:  dur,
		// rchn:       make(chan HeartbeatData, 10000),
		// signal:     make(chan bool, 1),
		errHandler: fn,
		datachn:    make(chan byte, 4*1024),
		once:       &sync.Once{},
		rerrchn:    make(chan error, 1),
		// rbuf:       bytes.Buffer{},
	}
	hrw.heartbeat()
	hrw.reader()
	return
}

func (rw *HeartbeatReadWriter) Close() {
	CloseConn(rw.conn)
}
func (rw *HeartbeatReadWriter) reader() {
	go func() {
		//log.Printf("heartbeat read started")
		for {
			n, data, err := rw.read()
			if n == -1 {
				continue
			}
			//log.Printf("n:%d , data:%s ,err:%s", n, string(data), err)
			if err == nil {
				//fmt.Printf("write data %s\n", string(data))
				for _, b := range data {
					rw.datachn <- b
				}
			}
			if err != nil {
				//log.Printf("heartbeat reader err: %s", err)
				select {
				case rw.rerrchn <- err:
				default:
				}
				rw.once.Do(func() {
					rw.errHandler(err, rw)
				})
				break
			}
		}
		//log.Printf("heartbeat read exited")
	}()
}
func (rw *HeartbeatReadWriter) read() (n int, data []byte, err error) {
	var typ uint8
	err = binary.Read((*rw.conn), binary.LittleEndian, &typ)
	if err != nil {
		return
	}
	if typ == 0 {
		// log.Printf("heartbeat revecived")
		n = -1
		return
	}
	var dataLength uint32
	binary.Read((*rw.conn), binary.LittleEndian, &dataLength)
	_data := make([]byte, dataLength)
	// log.Printf("dataLength:%d , data:%s", dataLength, string(data))
	n, err = (*rw.conn).Read(_data)
	//log.Printf("n:%d , data:%s ,err:%s", n, string(data), err)
	if err != nil {
		return
	}
	if uint32(n) != dataLength {
		err = fmt.Errorf("read short data body")
		return
	}
	data = _data[:n]
	return
}
func (rw *HeartbeatReadWriter) heartbeat() {
	go func() {
		//log.Printf("heartbeat started")
		for {
			if rw.conn == nil || *rw.conn == nil {
				//log.Printf("heartbeat err: conn nil")
				break
			}
			rw.l.Lock()
			_, err := (*rw.conn).Write([]byte{0})
			rw.l.Unlock()
			if err != nil {
				//log.Printf("heartbeat err: %s", err)
				rw.once.Do(func() {
					rw.errHandler(err, rw)
				})
				break
			} else {
				// log.Printf("heartbeat send ok")
			}
			time.Sleep(time.Second * time.Duration(rw.dur))
		}
		//log.Printf("heartbeat exited")
	}()
}
func (rw *HeartbeatReadWriter) Read(p []byte) (n int, err error) {
	data := make([]byte, cap(p))
	for i := 0; i < cap(p); i++ {
		data[i] = <-rw.datachn
		n++
		//fmt.Printf("read  %d %v\n", i, data[:n])
		if len(rw.datachn) == 0 {
			n = i + 1
			copy(p, data[:n])
			return
		}
	}
	return
}
func (rw *HeartbeatReadWriter) Write(p []byte) (n int, err error) {
	defer rw.l.Unlock()
	rw.l.Lock()
	pkg := new(bytes.Buffer)
	binary.Write(pkg, binary.LittleEndian, uint8(1))
	binary.Write(pkg, binary.LittleEndian, uint32(len(p)))
	binary.Write(pkg, binary.LittleEndian, p)
	bs := pkg.Bytes()
	n, err = (*rw.conn).Write(bs)
	if err == nil {
		n = len(p)
	}
	return
}

type ConnManager struct {
	pool ConcurrentMap
	l    *sync.Mutex
}

func NewConnManager() ConnManager {
	cm := ConnManager{
		pool: NewConcurrentMap(),
		l:    &sync.Mutex{},
	}
	return cm
}
func (cm *ConnManager) Add(key, ID string, conn *net.Conn) {
	cm.pool.Upsert(key, nil, func(exist bool, valueInMap interface{}, newValue interface{}) interface{} {
		var conns ConcurrentMap
		if !exist {
			conns = NewConcurrentMap()
		} else {
			conns = valueInMap.(ConcurrentMap)
		}
		if conns.Has(ID) {
			v, _ := conns.Get(ID)
			(*v.(*net.Conn)).Close()
		}
		conns.Set(ID, conn)
		log.Printf("%s conn added", key)
		return conns
	})
}
func (cm *ConnManager) Remove(key string) {
	var conns ConcurrentMap
	if v, ok := cm.pool.Get(key); ok {
		conns = v.(ConcurrentMap)
		conns.IterCb(func(key string, v interface{}) {
			CloseConn(v.(*net.Conn))
		})
		log.Printf("%s conns closed", key)
	}
	cm.pool.Remove(key)
}
func (cm *ConnManager) RemoveOne(key string, ID string) {
	defer cm.l.Unlock()
	cm.l.Lock()
	var conns ConcurrentMap
	if v, ok := cm.pool.Get(key); ok {
		conns = v.(ConcurrentMap)
		if conns.Has(ID) {
			v, _ := conns.Get(ID)
			(*v.(*net.Conn)).Close()
			conns.Remove(ID)
			cm.pool.Set(key, conns)
			log.Printf("%s %s conn closed", key, ID)
		}
	}
}
func (cm *ConnManager) RemoveAll() {
	for _, k := range cm.pool.Keys() {
		cm.Remove(k)
	}
}
