[gorilla websocket github链接](https://github.com/gorilla/websocket) 

安装依赖
```
go get github.com/gorilla/websocket
go get github.com/gin-gonic/gin
go get github.com/gomodule/redigo
go get github.com/satori/go.uuid
```

启动: 默认ip和port, 127.0.0.1:8080
`go run main.go client.go hub.go`

自定ip和端口启动
`go run .\main.go .\hub.go .\client.go --addr="127.0.0.1:8080"`

tools.go文件代码如下：
```go
package main

import "github.com/gomodule/redigo/redis"

// GetRedisConn 获取redis链接
func GetRedisConn() (redis.Conn, error) {
	return redis.Dial(
		"tcp",
		"host:port",
		redis.DialPassword("pwd"),
		redis.DialDatabase(0),
	)
}
```
