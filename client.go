package main

import (
	"bytes"
	"fmt"
	uuid "github.com/satori/go.uuid"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/gorilla/websocket"
)

const (
	// 允许向对等方写入消息的时间
	writeWait = 10 * time.Second

	// 允许从对等方读取下一条 pong 消息的时间
	pongWait = 60 * time.Second

	// 在此期间向对等方发送 ping。必须小于 pongWait
	pingPeriod = (pongWait * 9) / 10

	// 对等方允许的最大消息大小。
	maxMessageSize = 512

	// 最大不活跃时间，超过此时间下线当前用户
	maxActiveSecond = 60 * 10
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

// Upgrader指定将http链接升级为websocket链接的参数, 同时调用upgrader的方法是安全的
var upgrader = websocket.Upgrader{
	//ReadBufferSize 和 WriteBufferSize 以字节为单位指定 IO 缓冲区大小。
	//如果缓冲区大小为零，则使用 HTTP 服务器分配的缓冲区。 IO 缓冲区大小不限制可以发送或接收的消息的大小。
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

var names = []string{
	"秦始皇", "汉武帝", "武松", "西门庆", "孙悟空", "如来佛祖",
	"潘金莲", "后羿", "夸父", "黄晓明", "罗永浩", "太白金星",
	"猪八戒", "林冲", "红孩儿", "观音菩萨", "刘邦", "项羽",
	"黑旋风", "张三", "李四", "王五", "赵六", "田七", "佟大为",
}

type UserInfo struct {
	Name string
}

// Client是websocket链接和集线器之间的中间人
type Client struct {
	hub *Hub

	// The websocket connection.
	conn *websocket.Conn

	// 客户端出站消息的缓冲通道
	send chan []byte

	// 用户信息
	userInfo *UserInfo

	// 监听用户是否活跃的channel
	isActiveChan chan bool
}

// readPump将消息从websocket链接泵到集线器
// readPump 将消息从 websocket 连接泵到集线器。该应用程序在每个连接的 goroutine 中运行 readPump。
// 应用程序通过执行来自该 goroutine 的所有读取来确保连接上最多有一个读取器。
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c  // 在集线器中注销当前用户
		c.conn.Close()
	}()
	// SetReadLimit 设置从对等方读取的消息的最大大小（以字节为单位）。
	// 如果消息超出限制，则连接向对等方发送关闭消息并将 ErrReadLimit 返回给应用程序。
	c.conn.SetReadLimit(maxMessageSize)
	// SetReadDeadline 设置底层网络连接的读取期限。读取超时后
	// websocket 连接状态已损坏，所有未来读取都将返回错误。 t 的零值意味着读取不会超时。
	c.conn.SetReadDeadline(time.Now().Add(pongWait))  //  60s: 允许从对等方读取下一条 pong 消息的时间
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })

	for {
		// 默认阻塞从websocket链接读取消息
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			// IsUnexpectedCloseError 返回布尔值，指示错误是否是 CloseError，其代码不在预期代码列表中。
			// 如果是未预期的关闭错误返回true，否则返回true
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}

			// 用户关闭了链接，判断是程序关闭还是用户浏览器关闭，如果是浏览器关闭，发送广播通知该用户已下线
			// 如果是程序超时关闭，那么就不需要发送通知了
			if _, ok := c.hub.clients[c]; ok {
				// 在集线器中注销当前用户, 以免当前用户在接收到广播消息
				c.hub.unregister <- c
				// 当前用户以关闭浏览器，广播下线当前用户
				c.hub.broadcast <- &BroadCast{content: []byte(fmt.Sprintf("【%s】下线了", c.userInfo.Name)), messageType: Offline}
			}

			break
		}
		// TrimSpace 通过切掉所有前导和尾随空格来返回 s 的子切片，如 Unicode 所定义。
		// Replace，参数为-1，将所有的换行符替换为空格
		message = bytes.TrimSpace(bytes.Replace(message, newline, space, -1))
		// 消息前面添加发送消息的用户名和时间
		message = append([]byte(fmt.Sprintf("【%s】%s", c.userInfo.Name, time.Now().Format("15:04:05："))), message...)
		c.hub.broadcast <-&BroadCast{content: message, messageType: GeneralMsg}

		// 当前用户是活跃的
		c.isActiveChan <- true
	}
}

// 将消息从集线器泵送到websocket链接
// 为每个链接启动一个writePump的goroutine
// 应用程序通过执行来自该 goroutine 的所有写入来确保最多有一个写入者连接。
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)  // 54s, 在此期间向对等方发送 ping。必须小于 pongWait
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			// SetWriteDeadline 设置底层网络连接的写入期限。写入超时后，websocket 状态已损坏，
			// 以后的所有写入都将返回错误。 t 的零值意味着写入不会超时。
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))  // writeWait: 10s 允许向对等方写入消息的时间
			if !ok {  // 如果ok返回的是false，说明集线器已经关闭了channel
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// NextWriter 为要发送的下一条消息返回一个编写器。 writer 的 Close 方法将完整的消息刷新到网络。
			//一个连接上最多可以有一个打开的写入器。如果应用程序尚未关闭前一个写入器，NextWriter 将关闭它。
			//支持所有消息类型（TextMessage、BinaryMessage、CloseMessage、PingMessage 和 PongMessage）
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// 将排队的聊天信息添加到当前的websocket信息当中
			// 为了在高负载下提高效率，将发送通道中挂起的聊天信息合并为当个websocket信息
			// 这减少了系统调用的数量和通过网络发送的次数
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write(newline)
				w.Write(<-c.send)
			}

			// w(writer)的Close方法将完整的消息刷新到网络
			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:  // 54s, 在此期间向对等方发送 ping。必须小于 pongWait
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))  // writeWait: 10s 允许向对等方写入消息的时间
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// serveWs处理来自对等方的websocket请求
func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	// 升级将http服务器链接升级到websocket协议
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	math.NaN()
	nameSlice, _ := uuid.NewV4().MarshalText()
	client := &Client{hub: hub, conn: conn, send: make(chan []byte, 256),
		//userInfo: &UserInfo{Name: names[rand.Intn(len(names))]},
		userInfo: &UserInfo{Name: names[rand.Intn(len(names))] + "-" + strings.Split(string(nameSlice), "-")[4]},
		isActiveChan: make(chan bool),
	}
	// 将当前客户端注册到hub中
	// 为啥不直接注册到hub的map中去，而是注册到hub的通道中去，因为map是并发不安全的
	client.hub.register <- client

	// 广播用户上线
	client.hub.broadcast <- &BroadCast{content: []byte(fmt.Sprintf("【%s】上线了", client.userInfo.Name)), messageType: Online}

	// 通过在新的goroutine中完成所有工作，允许收集调用者引用的内存
	go client.writePump()
	go client.readPump()

	// 将最近的5条消息发送给刚刚上线的用户
	redisConn, err := GetRedisConn()
	if err != nil {
		fmt.Println("serveWs中链接redis失败", err.Error())
	}
	byteSlices, err := redis.ByteSlices(redisConn.Do("lrange", "websocket:message", 0, 4))
	if err != nil {
		fmt.Println("serveWs中获取websocket:message前5条失败", err.Error())
	}
	for i := len(byteSlices)-1; i >= 0; i--{
		client.send <- byteSlices[i]
	}

	// 监听用户是否活跃
	go client.isActiveUser()
}

// 监听用户是否活跃
func (c *Client) isActiveUser() {
	for {
		select{
		case <-c.isActiveChan:
		case <-time.After(time.Second * maxActiveSecond):
			// 判断当前用户是否以下线，如果未下线，广播通知并将其下线，然后return结束，如果已下线, 直接return结束就好了
			if _, ok := c.hub.clients[c]; ok {
				// 在集线器中注销当前用户
				c.hub.unregister <- c
				// 当前用户不活跃，广播下线当前用户
				c.hub.broadcast <- &BroadCast{content: []byte(fmt.Sprintf("【%s】超过【%d】秒不活跃，已被下线", c.userInfo.Name, maxActiveSecond)), messageType: ForcedOffline}
				// 关闭当前客户端的websocket链接
				c.conn.Close()
			}
			return
		}
	}
}
