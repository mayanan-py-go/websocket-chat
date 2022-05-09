package main

import "fmt"

type MessageType int

// 增加消息类型
const (
	GeneralMsg MessageType = iota
	Online
	Offline
	ForcedOffline
)

type BroadCast struct {
	content []byte
	messageType MessageType
}

// hub维护一组活动客户端，并向客户端广播消息
type Hub struct {
	clients map[*Client]bool

	// 来自客户端的入站消息
	broadcast chan *BroadCast

	// 注册来自客户端的请求
	register chan *Client

	// 取消注册来自客户端的请求
	unregister chan *Client
}

func newHub() *Hub {
	return &Hub{
		broadcast:  make(chan *BroadCast),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:  // 注册来自客户端的请求
			h.clients[client] = true
		case client := <-h.unregister:  // 注销来自客户端的请求
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)  // 关闭客户端出站消息的缓冲通道
			}
		case message := <-h.broadcast:  // 接收所有客户端的入站消息的广播通道
			for client := range h.clients {  // 循环将消息写入客户端的出站出站缓冲通道
				select {
				case client.send <- message.content:
				default:  // 如果客户端的出站缓冲区已满，则hub假定客户端卡住或已死，
				          // 在这种情况下，hub关闭客户端出站通道，并注销客户端
					close(client.send)
					delete(h.clients, client)
				}
			}
			// 将普通消息GENERAL_MSG保存到redis的列表中
			if message.messageType == GeneralMsg {
				redisConn, err := GetRedisConn()
				if err != nil {
					fmt.Println("hub.run中链接redis失败", err.Error())
				}
				_, err = redisConn.Do("lpush", "websocket:message", message.content)
				if err != nil {
					fmt.Println("消息写入redis失败", string(message.content), err.Error())
				}
			}
		}
	}
}
