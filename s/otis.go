/*内网穿透代理
访问限制ip的内网web服务
这是服务端(对于传统代理来说是客户端)
建立三个tcp侦听口，分别接受：
控制信道接入，客户-服务端数据信道接入，浏览器-服务端http信道接入
客户端-服务端通讯采用rc4加密
*/
package main

import (
	"crypto/rc4"
	"flag"
	"fmt"
	"otiWeb/katcp"
	"time"

	"github.com/buger/jsonparser"

	//	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var pwd string = "协议中的头信息和正文是采用空行分开"
var ffilename = flag.String("f", "s-config.json", "配置文件名")

var fileName string
var cmdPort string
var dataPort string
var webPort string

var AddDataConn = []byte{0x05, 0x05, 0x05, 0x05} //通知客户端呼入一个数据通道

type Rc4 struct {
	C *rc4.Cipher
}

type ProxyListener struct {
	cmdListener  *net.TCPListener
	dataListener *net.TCPListener
	webListener  *net.TCPListener
}
type ProxyClient struct {
	cmdConn     *katcp.KaConn
	dataConns   []*net.TCPConn
	ip          net.IP
	ipS         string
	domainNames []string //客户端需代理的域名列表
}

type Browser struct {
	webConns []*net.TCPConn
}

type ProxyServer struct {
	pClient  *ProxyClient
	browser  *Browser
	listener *ProxyListener
	webCount int
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
	cmdPort, err = jsonparser.GetString(jsondata, "cmdPort")
	if err != nil {
		fmt.Println("配置文件中无服务端口号", cmdPort, err)
		return
	}
	cmdPort = ":" + cmdPort

	dataPort, err = jsonparser.GetString(jsondata, "dataPort")
	if err != nil {
		fmt.Println("配置文件中无服务端口号", dataPort, err)
		return
	}
	dataPort = ":" + dataPort

	webPort, err = jsonparser.GetString(jsondata, "webPort")
	if err != nil {
		fmt.Println("配置文件中无服务端口号", webPort, err)
		return
	}
	webPort = ":" + webPort

	pwd, err = jsonparser.GetString(jsondata, "password")
	if err != nil {
		fmt.Println("配置文件中无密码", err)
		return
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cmdTcpAddr, err := net.ResolveTCPAddr("tcp4", cmdPort)
	if err != nil {
		fmt.Println("cmdPort地址错", err)
		return
	}
	cmdTcpListener, err := net.ListenTCP("tcp", cmdTcpAddr)
	if err != nil {
		fmt.Println("cmdPort开始tcp侦听出错", err)
	}

	dataTcpAddr, err := net.ResolveTCPAddr("tcp4", dataPort)
	if err != nil {
		fmt.Println("dataPort 地址错", err)
		return
	}
	dataTcpListener, err := net.ListenTCP("tcp", dataTcpAddr)
	if err != nil {
		fmt.Println("dataPort 开始tcp侦听出错", err)
	}

	webTcpAddr, err := net.ResolveTCPAddr("tcp4", webPort)
	if err != nil {
		fmt.Println("webPort 地址错", err)
		return
	}
	webTcpListener, err := net.ListenTCP("tcp", webTcpAddr)
	if err != nil {
		fmt.Println("webPort 开始tcp侦听出错", err)
	}

	fmt.Println("代理客户端已启动，服务端口是:", "cmdPort:", cmdPort, "dataPort:", dataPort, "webPort:", webPort)

	pServer := &ProxyServer{}
	pServer.webCount = 0

	pServer.listener = &ProxyListener{}
	pServer.pClient = &ProxyClient{}
	pServer.browser = &Browser{}

	pServer.listener.cmdListener = cmdTcpListener
	pServer.listener.dataListener = dataTcpListener
	pServer.listener.webListener = webTcpListener

	pServer.startListen()

	hold := make(chan int)
	hold <- 1
	hold <- 2
}

func (pS *ProxyServer) startListen() {
	pS.acceptCmdTcp(pS.listener.cmdListener)
}

//接受一个命令信道的呼入
func (pS *ProxyServer) acceptCmdTcp(listener *net.TCPListener) {
	fmt.Println("准备接受命令呼入：", time.Now())
	client, err := listener.AcceptTCP()
	fmt.Println("接受程序执行结束：", time.Now(), err)

	tk := time.NewTicker(katcp.HB.EchoInterval)
	fmt.Println("准备接受命令呼入：", time.Now())
	for range tk.C {
		client, err = listener.AcceptTCP()
		if err != nil {
			if client != nil {
				client.Close()
				client = nil
			}
			fmt.Println("接受程序执行出错：", time.Now(), err)
		} else {
			break
		}

	}
	fmt.Println("接受程序执行结束：", time.Now(), err)

	if pS.pClient == nil {
		pS.pClient = &ProxyClient{}
	}
	pS.pClient.cmdConn = katcp.GetKaConn(client, listener)
	fmt.Println(pS.pClient.cmdConn)
	go pS.pClient.cmdConn.StartEchoServer()
	fmt.Println("接到一个客户端呼入")
	go pS.acceptWebTcpLoop(pS.listener.webListener)

	log.Println("当前协程数量：", runtime.NumGoroutine())

}
func (pS *ProxyServer) acceptWebTcpLoop(listener *net.TCPListener) {
	for {
		client, err := listener.AcceptTCP()
		if err != nil {
			if client != nil {
				client.Close()
			}
			log.Panic(err)
		}
		go pS.handleWeb(client)
		log.Println("当前协程数量：", runtime.NumGoroutine())
	}
}

func (pS *ProxyServer) handleWeb(webClient *net.TCPConn) {

	defer webClient.Close()
	c1, _ := rc4.NewCipher([]byte(pwd))
	c2, _ := rc4.NewCipher([]byte(pwd))
	pcTos := &Rc4{c1}
	psToc := &Rc4{c2}

	if webClient == nil {
		fmt.Println("tcp连接空")
		return
	}
	//通知客户端创建一个新的数据通道
	pS.pClient.cmdConn.Write(AddDataConn)
	dataClient, err := pS.listener.dataListener.AcceptTCP()
	pS.webCount++
	fmt.Println("webCount=:", pS.webCount)

	for err != nil {
		fmt.Println("接收数据连接失败,正在等待重新连接。。。。")
		pS.pClient.cmdConn.Write(AddDataConn)
		dataClient, err = pS.listener.dataListener.AcceptTCP()
		pS.webCount++
		fmt.Println("webCount=:", pS.webCount)
	}

	defer dataClient.Close()
	//进行转发,这两句顺序不能倒，否则tcp连接不会自动关掉，会越来越多，只有等系统的tcp,timout到来
	//才能关闭掉。
	go psToc.encryptCopy(webClient, dataClient) //代理服务端发过来的是密文，编码后就成了明文，并传给浏览器
	pcTos.encryptCopy(dataClient, webClient)    //客户端收到的是明文，编码后就成了密文并传给代理的服务端

}

func (c *Rc4) encryptCopy(dst *net.TCPConn, src *net.TCPConn) {
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 4096)
	var err error
	n := 0
	for n, err = src.Read(buf); err == nil && n > 0; n, err = src.Read(buf) {
		//5秒无数据传输就断掉连接
		dst.SetDeadline(time.Now().Add(time.Second * 5))
		src.SetDeadline(time.Now().Add(time.Second * 5))
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

//根据ip查找一个proxyClient，用于检查是否查客户端已经连接了服务端
func findClientByIp(ip string) *net.TCPConn {
	return nil
}

//根据需访问的域名查找一个proxyClient,
func findClientByDomainName(dName string) *net.TCPConn {
	return nil
}
