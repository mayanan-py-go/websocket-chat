package main

import (
	"flag"
	"github.com/gin-gonic/gin"
	"net/http"
)

var addr = flag.String("addr", "127.0.0.1:8080", "http service address")

func main() {
	flag.Parse()

	hub := newHub()
	go hub.run()  // 开启协程监听：客户端注册请求，客户端注销请求，客户端发送消息到广播请求

	r := gin.Default()
	r.LoadHTMLFiles("home.html")

	r.GET("/", func(context *gin.Context) {
		context.HTML(http.StatusOK, "home.html", nil)
	})

	r.GET("/ws", func(context *gin.Context) {
		serveWs(hub, context.Writer, context.Request)
	})

	err := r.Run(*addr)
	if err != nil {
		return
	}
}
