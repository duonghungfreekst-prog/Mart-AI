package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	sql.Register("mysql", &pureMySQLDriver{})
}

const (
	mysqlCapLongPassword              uint32 = 0x00000001
	mysqlCapFoundRows                 uint32 = 0x00000002
	mysqlCapLongFlag                  uint32 = 0x00000004
	mysqlCapConnectWithDB             uint32 = 0x00000008
	mysqlCapProtocol41                uint32 = 0x00000200
	mysqlCapSSL                       uint32 = 0x00000800
	mysqlCapTransactions              uint32 = 0x00002000
	mysqlCapSecureConnection          uint32 = 0x00008000
	mysqlCapMultiStatements           uint32 = 0x00010000
	mysqlCapMultiResults              uint32 = 0x00020000
	mysqlCapPluginAuth                uint32 = 0x00080000
	mysqlCapPluginAuthLenencClientDat uint32 = 0x00200000

	mysqlComQuit  byte = 0x01
	mysqlComInit  byte = 0x02
	mysqlComQuery byte = 0x03
	mysqlComPing  byte = 0x0e

	mysqlTypeDecimal    byte = 0x00
	mysqlTypeTiny       byte = 0x01
	mysqlTypeShort      byte = 0x02
	mysqlTypeLong       byte = 0x03
	mysqlTypeFloat      byte = 0x04
	mysqlTypeDouble     byte = 0x05
	mysqlTypeNull       byte = 0x06
	mysqlTypeTimestamp  byte = 0x07
	mysqlTypeLongLong   byte = 0x08
	mysqlTypeInt24      byte = 0x09
	mysqlTypeYear       byte = 0x0d
	mysqlTypeNewDecimal byte = 0xf6
)

type mysqlDSNConfig struct {
	User     string
	Password string
	Host     string
	Port     string
	Database string
	UseTLS   bool
}

func parseMySQLConnectionURL(rawURL string) (*mysqlDSNConfig, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("invalid mysql url: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "mysql") {
		return nil, fmt.Errorf("unsupported scheme %q (expected mysql://)", u.Scheme)
	}
	user := ""
	pass := ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		dbName = "defaultdb"
	}
	q := u.Query()
	sslMode := strings.ToUpper(strings.TrimSpace(q.Get("ssl-mode")))
	tlsParam := strings.ToLower(strings.TrimSpace(q.Get("tls")))
	useTLS := sslMode == "REQUIRED" || sslMode == "VERIFY_CA" || sslMode == "VERIFY_IDENTITY" ||
		tlsParam == "true" || tlsParam == "skip-verify" || tlsParam == "preferred" ||
		strings.Contains(strings.ToLower(host), "aivencloud.com")

	return &mysqlDSNConfig{
		User:     user,
		Password: pass,
		Host:     host,
		Port:     port,
		Database: dbName,
		UseTLS:   useTLS,
	}, nil
}

type pureMySQLDriver struct{}

func (d *pureMySQLDriver) Open(name string) (driver.Conn, error) {
	cfg, err := parseMySQLConnectionURL(name)
	if err != nil {
		return nil, err
	}
	return dialMySQLConn(cfg)
}

type mysqlConn struct {
	mu     sync.Mutex
	conn   net.Conn
	cfg    *mysqlDSNConfig
	closed bool
}

func dialMySQLConn(cfg *mysqlDSNConfig) (*mysqlConn, error) {
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	rawConn, err := net.DialTimeout("tcp", addr, 12*time.Second)
	if err != nil {
		return nil, fmt.Errorf("mysql dial %s failed: %w", addr, err)
	}
	_ = rawConn.SetDeadline(time.Now().Add(20 * time.Second))

	mc := &mysqlConn{
		conn: rawConn,
		cfg:  cfg,
	}
	if err := mc.handshake(); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	_ = mc.conn.SetDeadline(time.Time{})
	return mc, nil
}

func (c *mysqlConn) readPacket() ([]byte, byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(c.conn, header[:]); err != nil {
		return nil, 0, err
	}
	length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
	seq := header[3]

	payload := make([]byte, length)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return nil, seq, err
	}

	// Xử lý gói tin lớn hơn 16MB
	for length == 0xffffff {
		if _, err := io.ReadFull(c.conn, header[:]); err != nil {
			return nil, seq, err
		}
		length = int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
		seq = header[3]
		chunk := make([]byte, length)
		if _, err := io.ReadFull(c.conn, chunk); err != nil {
			return nil, seq, err
		}
		payload = append(payload, chunk...)
	}
	return payload, seq, nil
}

func (c *mysqlConn) writePacket(payload []byte, seq byte) error {
	length := len(payload)
	header := []byte{
		byte(length),
		byte(length >> 8),
		byte(length >> 16),
		seq,
	}
	buf := make([]byte, 4+length)
	copy(buf[:4], header)
	copy(buf[4:], payload)
	_, err := c.conn.Write(buf)
	return err
}

func (c *mysqlConn) handshake() error {
	pkt, seq, err := c.readPacket()
	if err != nil {
		return fmt.Errorf("read initial handshake failed: %w", err)
	}
	if len(pkt) < 32 {
		return errors.New("mysql handshake packet too short")
	}
	if pkt[0] == 0xff {
		return parseMySQLErrPacket(pkt)
	}

	// Skip protocol_version (1 byte) + server_version (null-terminated)
	pos := 1
	for pos < len(pkt) && pkt[pos] != 0 {
		pos++
	}
	pos++ // skip NUL
	if pos+4+8+1+2 > len(pkt) {
		return errors.New("malformed handshake packet")
	}
	pos += 4 // connection_id
	scramble := make([]byte, 8, 20)
	copy(scramble, pkt[pos:pos+8])
	pos += 8
	pos++ // filler 0x00

	capLower := uint32(binary.LittleEndian.Uint16(pkt[pos : pos+2]))
	pos += 2

	var capUpper uint32
	var authPluginLen int
	if pos+1+2+2+1+10 <= len(pkt) {
		pos++ // character_set
		pos += 2 // status_flags
		capUpper = uint32(binary.LittleEndian.Uint16(pkt[pos : pos+2]))
		pos += 2
		authPluginLen = int(pkt[pos])
		pos += 1 + 10 // auth_plugin_len + 10 reserved bytes
	}
	serverCaps := capLower | (capUpper << 16)

	if (serverCaps & mysqlCapSecureConnection) != 0 {
		readLen := 13
		if authPluginLen > 8 {
			readLen = authPluginLen - 8
			if readLen < 13 {
				readLen = 13
			}
		}
		if pos+readLen <= len(pkt) {
			part2 := pkt[pos : pos+readLen]
			if len(part2) > 0 && part2[len(part2)-1] == 0 {
				part2 = part2[:len(part2)-1]
			}
			scramble = append(scramble, part2...)
			pos += readLen
		}
	}

	pluginName := "mysql_native_password"
	if (serverCaps&mysqlCapPluginAuth) != 0 && pos < len(pkt) {
		end := pos
		for end < len(pkt) && pkt[end] != 0 {
			end++
		}
		if end > pos {
			pluginName = string(pkt[pos:end])
		}
	}

	clientFlags := mysqlCapLongPassword |
		mysqlCapFoundRows |
		mysqlCapLongFlag |
		mysqlCapProtocol41 |
		mysqlCapTransactions |
		mysqlCapSecureConnection |
		mysqlCapMultiStatements |
		mysqlCapMultiResults |
		mysqlCapPluginAuth
	if c.cfg.Database != "" {
		clientFlags |= mysqlCapConnectWithDB
	}

	nextSeq := seq + 1

	// Bắt tay TLS 1.2+ nếu sử dụng SSL/TLS (Aiven MySQL yêu cầu ssl-mode=REQUIRED)
	if c.cfg.UseTLS && (serverCaps&mysqlCapSSL) != 0 {
		clientFlags |= mysqlCapSSL
		sslPkt := make([]byte, 32)
		binary.LittleEndian.PutUint32(sslPkt[0:4], clientFlags)
		binary.LittleEndian.PutUint32(sslPkt[4:8], 16*1024*1024)
		sslPkt[8] = 45 // utf8mb4_general_ci
		if err := c.writePacket(sslPkt, nextSeq); err != nil {
			return fmt.Errorf("write ssl request failed: %w", err)
		}
		nextSeq++

		tlsConn := tls.Client(c.conn, &tls.Config{
			ServerName:         c.cfg.Host,
			InsecureSkipVerify: true, // Aiven sử dụng Project CA tự ký cho ssl-mode=REQUIRED
			MinVersion:         tls.VersionTLS12,
		})
		if err := tlsConn.Handshake(); err != nil {
			return fmt.Errorf("mysql tls handshake failed: %w", err)
		}
		c.conn = tlsConn
	}

	authResp := computeMySQLAuthResponse(pluginName, c.cfg.Password, scramble)

	// Tạo gói HandshakeResponse41
	resp := make([]byte, 32, 128)
	binary.LittleEndian.PutUint32(resp[0:4], clientFlags)
	binary.LittleEndian.PutUint32(resp[4:8], 16*1024*1024)
	resp[8] = 45 // utf8mb4_general_ci

	resp = append(resp, []byte(c.cfg.User)...)
	resp = append(resp, 0)

	resp = append(resp, byte(len(authResp)))
	resp = append(resp, authResp...)

	if c.cfg.Database != "" {
		resp = append(resp, []byte(c.cfg.Database)...)
		resp = append(resp, 0)
	}

	resp = append(resp, []byte(pluginName)...)
	resp = append(resp, 0)

	if err := c.writePacket(resp, nextSeq); err != nil {
		return fmt.Errorf("write handshake response failed: %w", err)
	}

	for {
		authPkt, rSeq, err := c.readPacket()
		if err != nil {
			return fmt.Errorf("read auth response failed: %w", err)
		}
		if len(authPkt) == 0 {
			return errors.New("empty auth packet")
		}
		switch authPkt[0] {
		case 0x00:
			// OK_Packet
			return nil
		case 0xff:
			return parseMySQLErrPacket(authPkt)
		case 0xfe:
			// AuthSwitchRequest
			pos := 1
			for pos < len(authPkt) && authPkt[pos] != 0 {
				pos++
			}
			newPlugin := string(authPkt[1:pos])
			pos++
			newScramble := authPkt[pos:]
			if len(newScramble) > 0 && newScramble[len(newScramble)-1] == 0 {
				newScramble = newScramble[:len(newScramble)-1]
			}
			pluginName = newPlugin
			switchResp := computeMySQLAuthResponse(newPlugin, c.cfg.Password, newScramble)
			if err := c.writePacket(switchResp, rSeq+1); err != nil {
				return err
			}
		case 0x01:
			// AuthMoreData (caching_sha2_password)
			if len(authPkt) >= 2 && authPkt[1] == 0x03 {
				// Fast auth success, tiếp tục đọc OK_Packet ở vòng lặp kế tiếp
				continue
			}
			if len(authPkt) >= 2 && authPkt[1] == 0x04 {
				// Full authentication over TLS: gửi password kết thúc bằng NUL byte
				cleartext := append([]byte(c.cfg.Password), 0)
				if err := c.writePacket(cleartext, rSeq+1); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("unsupported mysql AuthMoreData status: 0x%x", authPkt[1])
		default:
			return fmt.Errorf("unexpected mysql auth packet header: 0x%x", authPkt[0])
		}
	}
}

func computeMySQLAuthResponse(plugin, password string, scramble []byte) []byte {
	if password == "" {
		return nil
	}
	if plugin == "caching_sha2_password" {
		h1 := sha256.Sum256([]byte(password))
		h2 := sha256.Sum256(h1[:])
		h3 := sha256.New()
		h3.Write(h2[:])
		h3.Write(scramble)
		sum3 := h3.Sum(nil)
		out := make([]byte, 32)
		for i := 0; i < 32; i++ {
			out[i] = h1[i] ^ sum3[i]
		}
		return out
	}
	// Default: mysql_native_password
	h1 := sha1.Sum([]byte(password))
	h2 := sha1.Sum(h1[:])
	h3 := sha1.New()
	h3.Write(scramble)
	h3.Write(h2[:])
	sum3 := h3.Sum(nil)
	out := make([]byte, 20)
	for i := 0; i < 20; i++ {
		out[i] = h1[i] ^ sum3[i]
	}
	return out
}

func parseMySQLErrPacket(pkt []byte) error {
	if len(pkt) < 3 {
		return errors.New("mysql error")
	}
	errno := binary.LittleEndian.Uint16(pkt[1:3])
	pos := 3
	if pos < len(pkt) && pkt[pos] == '#' {
		if pos+6 <= len(pkt) {
			pos += 6
		}
	}
	return fmt.Errorf("MySQL Error %d: %s", errno, string(pkt[pos:]))
}

func readLenEncInt(b []byte, pos int) (uint64, int, bool) {
	if pos >= len(b) {
		return 0, pos, true
	}
	first := b[pos]
	switch first {
	case 0xfb:
		return 0, pos + 1, true // NULL
	case 0xfc:
		if pos+3 > len(b) {
			return 0, len(b), false
		}
		return uint64(binary.LittleEndian.Uint16(b[pos+1 : pos+3])), pos + 3, false
	case 0xfd:
		if pos+4 > len(b) {
			return 0, len(b), false
		}
		v := uint64(b[pos+1]) | uint64(b[pos+2])<<8 | uint64(b[pos+3])<<16
		return v, pos + 4, false
	case 0xfe:
		if pos+9 > len(b) {
			return 0, len(b), false
		}
		return binary.LittleEndian.Uint64(b[pos+1 : pos+9]), pos + 9, false
	default:
		return uint64(first), pos + 1, false
	}
}

func readLenEncString(b []byte, pos int) (string, int, bool) {
	length, newPos, isNull := readLenEncInt(b, pos)
	if isNull {
		return "", newPos, true
	}
	end := newPos + int(length)
	if end > len(b) {
		end = len(b)
	}
	return string(b[newPos:end]), end, false
}

func escapeMySQLString(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	sb.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case 0:
			sb.WriteString(`\0`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\\':
			sb.WriteString(`\\`)
		case '\'':
			sb.WriteString(`\'`)
		case '"':
			sb.WriteString(`\"`)
		case '\x1a':
			sb.WriteString(`\Z`)
		default:
			sb.WriteByte(ch)
		}
	}
	sb.WriteByte('\'')
	return sb.String()
}

func interpolateMySQLQuery(query string, args []driver.Value) (string, error) {
	if len(args) == 0 {
		return query, nil
	}
	var sb strings.Builder
	sb.Grow(len(query) + len(args)*16)
	argIdx := 0
	inSingleQuote := false
	inDoubleQuote := false

	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\\' && i+1 < len(query) {
			sb.WriteByte(ch)
			i++
			sb.WriteByte(query[i])
			continue
		}
		if ch == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			sb.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			sb.WriteByte(ch)
			continue
		}
		if ch == '?' && !inSingleQuote && !inDoubleQuote {
			if argIdx >= len(args) {
				return "", fmt.Errorf("not enough args for query placeholders (have %d)", len(args))
			}
			val := args[argIdx]
			argIdx++
			switch v := val.(type) {
			case nil:
				sb.WriteString("NULL")
			case int64:
				sb.WriteString(strconv.FormatInt(v, 10))
			case float64:
				if math.IsNaN(v) || math.IsInf(v, 0) {
					sb.WriteString("NULL")
				} else {
					sb.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
				}
			case bool:
				if v {
					sb.WriteByte('1')
				} else {
					sb.WriteByte('0')
				}
			case []byte:
				sb.WriteString(escapeMySQLString(string(v)))
			case string:
				sb.WriteString(escapeMySQLString(v))
			case time.Time:
				sb.WriteString(escapeMySQLString(v.UTC().Format("2006-01-02 15:04:05")))
			default:
				sb.WriteString(escapeMySQLString(fmt.Sprintf("%v", v)))
			}
			continue
		}
		sb.WriteByte(ch)
	}
	return sb.String(), nil
}

var (
	reExcludedCol = regexp.MustCompile(`(?i)\bexcluded\.([a-zA-Z0-9_]+)`)
	reOnConflict  = regexp.MustCompile(`(?i)\bON\s+CONFLICT\s*\([^)]+\)\s*DO\s+UPDATE\s+SET\b`)
	reDatetimeNow = regexp.MustCompile(`(?i)\bdatetime\s*\(\s*'now'\s*\)`)
	reSqliteVer   = regexp.MustCompile(`(?i)\bsqlite_version\s*\(\s*\)`)
)

// rewriteSQLForMySQL tự động chuyển đổi cú pháp SQLite/SQL40 sang MySQL 8.0 chuẩn.
func rewriteSQLForMySQL(rawSQL string) string {
	s := strings.TrimSpace(rawSQL)
	if strings.EqualFold(s, "PRAGMA journal_mode") || strings.EqualFold(s, "PRAGMA journal_mode;") {
		return "SELECT 'WAL'"
	}
	s = reSqliteVer.ReplaceAllString(s, "VERSION()")
	s = reDatetimeNow.ReplaceAllString(s, "DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%d %H:%i:%s')")
	if reOnConflict.MatchString(s) {
		s = reOnConflict.ReplaceAllString(s, "ON DUPLICATE KEY UPDATE")
		s = reExcludedCol.ReplaceAllString(s, "VALUES($1)")
	}
	return s
}

func (c *mysqlConn) Prepare(query string) (driver.Stmt, error) {
	return &mysqlStmt{conn: c, query: query}, nil
}

func (c *mysqlConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	_ = c.conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	_ = c.writePacket([]byte{mysqlComQuit}, 0)
	return c.conn.Close()
}

func (c *mysqlConn) Begin() (driver.Tx, error) {
	if _, err := c.execInternal("START TRANSACTION", nil); err != nil {
		return nil, err
	}
	return &mysqlTx{conn: c}, nil
}

func (c *mysqlConn) Exec(query string, args []driver.Value) (driver.Result, error) {
	return c.execInternal(query, args)
}

func (c *mysqlConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	return c.queryInternal(query, args)
}

func (c *mysqlConn) execInternal(query string, args []driver.Value) (driver.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, driver.ErrBadConn
	}
	rewritten := rewriteSQLForMySQL(query)
	finalSQL, err := interpolateMySQLQuery(rewritten, args)
	if err != nil {
		return nil, err
	}

	_ = c.conn.SetDeadline(time.Now().Add(20 * time.Second))
	defer c.conn.SetDeadline(time.Time{})

	cmdPkt := append([]byte{mysqlComQuery}, []byte(finalSQL)...)
	if err := c.writePacket(cmdPkt, 0); err != nil {
		return nil, driver.ErrBadConn
	}

	pkt, _, err := c.readPacket()
	if err != nil {
		return nil, driver.ErrBadConn
	}
	if len(pkt) == 0 {
		return nil, errors.New("empty response from mysql")
	}
	switch pkt[0] {
	case 0x00:
		affected, pos, _ := readLenEncInt(pkt, 1)
		lastID, _, _ := readLenEncInt(pkt, pos)
		return &mysqlResult{
			affectedRows: int64(affected),
			insertID:     int64(lastID),
		}, nil
	case 0xff:
		return nil, parseMySQLErrPacket(pkt)
	default:
		// Trường hợp câu lệnh trả về resultset trong Exec: xả hết các gói tin
		_, err := c.drainResultSet(pkt)
		if err != nil {
			return nil, err
		}
		return &mysqlResult{}, nil
	}
}

func (c *mysqlConn) queryInternal(query string, args []driver.Value) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, driver.ErrBadConn
	}
	rewritten := rewriteSQLForMySQL(query)
	finalSQL, err := interpolateMySQLQuery(rewritten, args)
	if err != nil {
		return nil, err
	}

	_ = c.conn.SetDeadline(time.Now().Add(25 * time.Second))
	defer c.conn.SetDeadline(time.Time{})

	cmdPkt := append([]byte{mysqlComQuery}, []byte(finalSQL)...)
	if err := c.writePacket(cmdPkt, 0); err != nil {
		return nil, driver.ErrBadConn
	}

	pkt, _, err := c.readPacket()
	if err != nil {
		return nil, driver.ErrBadConn
	}
	if len(pkt) == 0 {
		return nil, errors.New("empty response from mysql")
	}
	if pkt[0] == 0xff {
		return nil, parseMySQLErrPacket(pkt)
	}
	if pkt[0] == 0x00 {
		return &mysqlRows{columns: []string{}, rows: [][]driver.Value{}}, nil
	}
	return c.drainResultSet(pkt)
}

func (c *mysqlConn) drainResultSet(firstPkt []byte) (*mysqlRows, error) {
	colCountU64, _, _ := readLenEncInt(firstPkt, 0)
	colCount := int(colCountU64)
	cols := make([]string, colCount)
	colTypes := make([]byte, colCount)

	for i := 0; i < colCount; i++ {
		cpkt, _, err := c.readPacket()
		if err != nil {
			return nil, err
		}
		pos := 0
		_, pos, _ = readLenEncString(cpkt, pos) // catalog
		_, pos, _ = readLenEncString(cpkt, pos) // schema
		_, pos, _ = readLenEncString(cpkt, pos) // table
		_, pos, _ = readLenEncString(cpkt, pos) // org_table
		name, pos, _ := readLenEncString(cpkt, pos)
		_, pos, _ = readLenEncString(cpkt, pos) // org_name
		cols[i] = name
		if pos+1+2+4+1 <= len(cpkt) {
			pos++ // 0x0c
			pos += 2 // charset
			pos += 4 // col length
			colTypes[i] = cpkt[pos]
		}
	}

	// Đọc gói EOF kết thúc phần định nghĩa cột
	eofPkt, _, err := c.readPacket()
	if err != nil {
		return nil, err
	}
	if len(eofPkt) > 0 && eofPkt[0] == 0xff {
		return nil, parseMySQLErrPacket(eofPkt)
	}

	var allRows [][]driver.Value
	for {
		rpkt, _, err := c.readPacket()
		if err != nil {
			return nil, err
		}
		if len(rpkt) == 0 {
			break
		}
		if rpkt[0] == 0xfe && len(rpkt) < 9 {
			// EOF Packet
			break
		}
		if rpkt[0] == 0xff {
			return nil, parseMySQLErrPacket(rpkt)
		}

		row := make([]driver.Value, colCount)
		pos := 0
		for i := 0; i < colCount; i++ {
			strVal, nextPos, isNull := readLenEncString(rpkt, pos)
			pos = nextPos
			if isNull {
				row[i] = nil
				continue
			}
			switch colTypes[i] {
			case mysqlTypeTiny, mysqlTypeShort, mysqlTypeLong, mysqlTypeLongLong, mysqlTypeInt24, mysqlTypeYear:
				if iv, errConv := strconv.ParseInt(strVal, 10, 64); errConv == nil {
					row[i] = iv
				} else {
					row[i] = strVal
				}
			case mysqlTypeFloat, mysqlTypeDouble, mysqlTypeDecimal, mysqlTypeNewDecimal:
				if fv, errConv := strconv.ParseFloat(strVal, 64); errConv == nil {
					row[i] = fv
				} else {
					row[i] = strVal
				}
			default:
				row[i] = strVal
			}
		}
		allRows = append(allRows, row)
	}

	return &mysqlRows{
		columns: cols,
		rows:    allRows,
	}, nil
}

type mysqlStmt struct {
	conn  *mysqlConn
	query string
}

func (s *mysqlStmt) Close() error { return nil }

func (s *mysqlStmt) NumInput() int { return -1 }

func (s *mysqlStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.conn.execInternal(s.query, args)
}

func (s *mysqlStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.queryInternal(s.query, args)
}

type mysqlTx struct {
	conn *mysqlConn
}

func (t *mysqlTx) Commit() error {
	_, err := t.conn.execInternal("COMMIT", nil)
	return err
}

func (t *mysqlTx) Rollback() error {
	_, err := t.conn.execInternal("ROLLBACK", nil)
	return err
}

type mysqlResult struct {
	insertID     int64
	affectedRows int64
}

func (r *mysqlResult) LastInsertId() (int64, error) { return r.insertID, nil }
func (r *mysqlResult) RowsAffected() (int64, error) { return r.affectedRows, nil }

type mysqlRows struct {
	columns []string
	rows    [][]driver.Value
	idx     int
}

func (r *mysqlRows) Columns() []string { return r.columns }
func (r *mysqlRows) Close() error      { return nil }
func (r *mysqlRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.idx]
	r.idx++
	for i := range dest {
		if i < len(row) {
			dest[i] = row[i]
		} else {
			dest[i] = nil
		}
	}
	return nil
}
