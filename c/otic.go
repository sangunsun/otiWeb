/*加密传输的proxy，采用RC4加密，
 */
package main

import (
	"bufio"
	"bytes"
	"crypto/rc4"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"otiWeb/katcp"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/buger/jsonparser"
)

type Rc4 struct {
	C *rc4.Cipher
}

var pwd string = "helloworld"
var ffilename = flag.String("f", "c-config.json", "配置文件名")

var fileName string
var serverIP string
var serverCmdPort string
var serverDataPort string
var httpLen int = 4096

var AddDataConn = []byte{0x05, 0x05, 0x05, 0x05} //通知客户端呼入一个数据通道

type ProxyServer struct {
	cmdConn *katcp.KaConn
	//	dataConn        *net.TCPConn
	recivevCmdCount int
}

func init() {

	flag.Parse()
	fileName = *ffilename
}

func main() {
	f, err := os.Open(getProcessDir() + fileName)
	if err != nil {
		f, err = os.Open(fileName)
		if err != nil {
			fmt.Println("打开配置文件失败")
			return
		}
	}
	var jsondata []byte
	var buf = make([]byte, 1)
	for n, err := f.Read(buf); err == nil && n > 0; n, err = f.Read(buf) {
		jsondata = append(jsondata, buf[0])
	}
	serverIP, err = jsonparser.GetString(jsondata, "serverIP")
	if err != nil {
		fmt.Println("配置文件中无服务器地址", serverIP, err)
		return
	}
	serverCmdPort, err = jsonparser.GetString(jsondata, "serverCmdPort")
	if err != nil {
		fmt.Println("配置文件中无服务器地址", serverCmdPort, err)
		return
	}
	serverDataPort, err = jsonparser.GetString(jsondata, "serverDataPort")
	if err != nil {
		fmt.Println("配置文件中无服务器地址", serverDataPort, err)
		return
	}
	pwd, err = jsonparser.GetString(jsondata, "password")
	if err != nil {
		fmt.Println("配置文件中无密码", err)
		return
	}
	ps := ProxyServer{}
	ps.recivevCmdCount = 0
	for {
		ps.startDail()
	}
}

func (ps *ProxyServer) startDail() {
	i := 1

	//  拨号到互联网上的透传服务器,拨不通，就打出提示消息并一直拨
	address := serverIP + ":" + serverCmdPort
	tcpaddr, err := net.ResolveTCPAddr("tcp4", address)
	if err != nil {
		log.Println("tcp命令地址错误", address, err)
	}

	//开始监听cmd控制信道过来的命令消息
	cmdBuf := make([]byte, katcp.HBLEGHT)
	tk := time.NewTicker(time.Second * 2)
	for range tk.C {
		if ps.cmdConn == nil {
			fmt.Println("拨号到服务器:", address, "...第 ", i, " 次")
			tcpaddr, err = net.ResolveTCPAddr("tcp4", address)
			if err != nil {
				log.Println("tcp命令地址错误", address, err)
			}

			serverCmdConn, err := net.DialTCP("tcp", nil, tcpaddr)
			i++
			if err != nil {
				log.Println("拨号服务器命令端口失败", err)
				continue
			}
			ps.cmdConn = katcp.GetKaConnC(serverCmdConn, address)
			fmt.Println("address:", address)
			go ps.cmdConn.StartEchoClient()

			fmt.Println("已连接到服务器。")
			break
		}
	}
	//拨号成功后进入读命令循环，如果读命令错误退出循环，依赖上层调用函数的循环重新拨号命令通道
	for {
		//clearBytesSlice(cmdBuf)

		n, err := ps.cmdConn.Read(cmdBuf)
		if err != nil {
			continue
		}
		ps.recivevCmdCount++
		fmt.Println("recieve cmd count=:", ps.recivevCmdCount)
		fmt.Println("接收到的命令是：", cmdBuf[:n], "想收到的命令是：", AddDataConn)
		switch bytes.Equal(cmdBuf[:n], AddDataConn) {
		case true:
			//向服务器申请一条数据通道，
			//拨号到web服务器
			//开始在web服务器和server之间进行转发
			//具体实现就是向服务器申请到一个tcpConn数据通道后，直接调用handleAServerConn就可以了
			//  拨号到互联网上的透传服务器
			addressData := serverIP + ":" + serverDataPort
			fmt.Println("服务器数据地址address:", addressData)
			tcpaddrData, err := net.ResolveTCPAddr("tcp4", addressData)
			if err != nil {
				log.Println("tcp数据地址错误", addressData, err)
				break
			}
			serverDataConn, err := net.DialTCP("tcp", nil, tcpaddrData)
			if err != nil {
				log.Println("拨号服务器数据端口失败", err)
				break
			}
			go handleDataConn(serverDataConn)
		default:
		}

	}

}
func handleDataConn(client *net.TCPConn) {

	defer client.Close()
	c1, _ := rc4.NewCipher([]byte(pwd))
	c2, _ := rc4.NewCipher([]byte(pwd))

	pcTos := &Rc4{c1} //从client端收到的是密文，这里的作用是解密
	psToc := &Rc4{c2} //web服务器传过来的是明文，这里的作用是加密

	if client == nil {
		return
	}
	//对客户端传来的密文进行解密，得到http报头, 得到web服务器的ip、端口等信息
	byteHeader := deCodereadSplitString(client, pcTos, []byte("\r\n\r\n"))
	fmt.Println("原始报头信息：", string(byteHeader))

	//取报头字节流后解析为结构化报头，方便获取想要的信息
	bfr := bufio.NewReader(strings.NewReader(string(byteHeader)))
	req, err := http.ReadRequest(bfr)
	if err != nil {
		log.Println("转换request失败", err)
		return
	}
	var method, host, address string
	method = req.Method
	host = req.Host
	//hostPortURL, err := url.Parse(host)
	fmt.Println("取request信息m:", method, "host:", host) //, "hostPortURL:", hostPortURL)
	if err != nil {
		log.Println(err)
		return
	}
	//取服务器域名（或IP）和端口号以便tcp拨号服务器
	hostPort := strings.Split(host, ":")
	if len(hostPort) < 2 {
		address = hostPort[0] + ":80"
	} else {
		address = host
	}

	//获得了请求的host和port，就开始拨号吧
	tcpaddr, err := net.ResolveTCPAddr("tcp4", address)
	if err != nil {
		log.Println("tcp地址错误", address, err)
		return
	}
	server, err := net.DialTCP("tcp", nil, tcpaddr)
	if err != nil {
		log.Println(err)
		return
	}
	if method == "CONNECT" {
		bufTemp := []byte("HTTP/1.1 200 Connection established\r\n\r\n")
		psToc.C.XORKeyStream(bufTemp, bufTemp)
		client.Write(bufTemp)
	} else {
		server.Write(byteHeader[0:len(byteHeader)]) //最始的报头信息已经解密了，这里直接转发给web
	}
	//接下来得到的都是还没有解密的信息，进行解密转发
	go pcTos.encryptCopy(server, client) //服务端收到的是密文，编码后就成了明文并传给web
	psToc.encryptCopy(client, server)    //web发过来的是明文，编码后就成了密文，并传给客户端
	server.Close()                       //这句倒是否是必要的？？？？
}

func deCodereadSplitString(r *net.TCPConn, coder *Rc4, delim []byte) []byte {
	var rs []byte
	lenth := len(delim)
	curByte := make([]byte, 1)

	//先读取分隔符长度-1个字节，以避免在下面循环中每次都要判断是否读够分隔符长度的字节。
	for k := 0; k < lenth-1; k++ {
		r.Read(curByte)
		coder.C.XORKeyStream(curByte, curByte) //把密文转成明文
		rs = append(rs, curByte[0])
		if len(rs) > httpLen {
			return rs
		}
	}

	//继续读后面的字节并开始进行查找是否已经接收到报头正文分隔符
	for n, err := r.Read(curByte); err == nil && n > 0; n, err = r.Read(curByte) {
		coder.C.XORKeyStream(curByte, curByte)
		rs = append(rs, curByte[0])
		if len(rs) > httpLen {
			return rs
		}
		var m int
		//从后向前逐个字节比较已读字节的最后几位是否和分隔符相同
		for m = 0; m < lenth; m++ {
			tt := len(rs)
			if rs[tt-1-m] != delim[lenth-1-m] {
				break
			}
		}
		if m == lenth {
			return rs
		}
	}
	return rs
}
func (c *Rc4) encryptCopy(dst *net.TCPConn, src *net.TCPConn) {
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 4096)
	var err error
	n := 0
	for n, err = src.Read(buf); err == nil && n > 0; n, err = src.Read(buf) {
		//5秒无数据传输就断掉连接
		dst.SetDeadline(time.Now().Add(time.Second * 10))
		src.SetDeadline(time.Now().Add(time.Second * 10))
		c.C.XORKeyStream(buf[:n], buf[:n])

		dst.Write(buf[:n])
	}

}
func getProcessDir() string {
	file, _ := exec.LookPath(os.Args[0])

	path, _ := filepath.Abs(file)

	if runtime.GOOS == "windows" {
		path = strings.Replace(path, "\\", "/", -1)
	}

	i := strings.LastIndex(path, "/")
	return string(path[0 : i+1])
}
func clearBytesSlice(b []byte) {
	for i := 0; i < len(b); i++ {
		b[i] = 0x00
	}
}
