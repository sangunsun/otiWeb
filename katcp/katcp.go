//Keep-Alive tcp,katcp即tcp长连接的意思 kacon.go
package katcp

import (
	"bytes"
	"sync"

	"fmt"
	"net"
	"time"
)

//长连接：用于自动维护一个tcp长连接
//客户端：主动发心跳数据，收心跳回复数据，更新lastEchoTime时间
//服务端: 收心跳数据,更新lastEchoTime时间
//两边：读到普通数据，更新lastEchoTime时间
//两边：写数据成功，更新lastEchoTime时间？？？
const (
	HBLEGHT = 4
)

type HeartBeat struct {
	EchoInterval time.Duration //心跳间隔
	ConnTimeOut  time.Duration //多久没有数据传输就废弃该连接
	EchoData     [HBLEGHT]byte //心跳发出数据
	ReplyData    [HBLEGHT]byte //心跳回复数据

}

var HB HeartBeat

type Buf struct {
	buf []byte
	bsp uint
	bep uint
}
type KaConn struct {
	*net.TCPConn
	lastEchoTime time.Time     //上一次收到心跳包时间
	echoInterval time.Duration //心跳间隔
	connTimeOut  time.Duration //多久没有数据传输就废弃该连接
	echoData     [HBLEGHT]byte //心跳发出数据
	replyData    [HBLEGHT]byte //心跳回复数据
	buf          chan byte     //保存心跳检测误读的业务数据，心跳检测只读四个字节
	echoBuf      chan byte
	cmdListenner *net.TCPListener //用于维护tcp连接的有效性，如果发现连接断开了，重新接收一个连接呼入
	serverAddr   *net.TCPAddr
	sync.Mutex
}
type KaConns struct {
	conns []*KaConn
}

//设置默认的全局心跳参数，用户使用的时候随时可以进行修改
func init() {
	HB.EchoInterval = time.Second * 2
	HB.ConnTimeOut = time.Second * 6
	for i := 0; i < HBLEGHT; i++ {
		HB.EchoData[i] = 0x00 //心跳查询包，四个0
	}
	for i := 0; i < HBLEGHT; i++ {
		HB.ReplyData[i] = 0x01 //心跳回复包，四个1
	}
}
func GetKaConn(conn *net.TCPConn, cmdListenner *net.TCPListener) *KaConn {
	//init()
	kac := &KaConn{}
	kac.TCPConn = conn
	kac.echoInterval = HB.EchoInterval
	kac.connTimeOut = HB.ConnTimeOut
	kac.echoData = HB.EchoData
	kac.replyData = HB.ReplyData
	kac.buf = make(chan byte, 1024) //误读缓冲区，就是读心跳数据的协程可能会误读到业务数据，如果发现误读了业务数据，把误读的数据放入这个缓冲区，供Read函数读取
	kac.echoBuf = make(chan byte, HBLEGHT)
	addrStr := conn.RemoteAddr().String()
	if cmdListenner != nil {
		kac.cmdListenner = cmdListenner
	}
	kac.serverAddr, _ = net.ResolveTCPAddr("tcp4", addrStr)
	kac.lastEchoTime = time.Now()
	return kac
}
func (kac *KaConn) reGetTcpConn() bool {
	fmt.Println("kac.reGetTcpConn: 加锁前")
	kac.Lock()
	defer kac.Unlock()
	if kac.TCPConn == nil {
		fmt.Println("kac.reGetTcpConn: 准备接收新的命令通道呼入")
		tcpClient, err := kac.cmdListenner.AcceptTCP()
		fmt.Println("kac.reGetTcpConn: 一个接收结果", err)
		for err != nil {

			tcpClient, err = kac.cmdListenner.AcceptTCP()
			fmt.Println("kac.reGetTcpConn: 一个接收结果", err.Error())
		}
		kac.TCPConn = tcpClient
		kac.lastEchoTime = time.Now()
		fmt.Println("kac.reGetTcpConn: 新接收了一条命令通道")
	}
	return true
}
func (kac *KaConn) reDailTcpConn() bool {
	kac.Lock()
	defer kac.Unlock()
	if kac.TCPConn == nil {
		tcpClient, err := net.DialTCP("tcp", nil, kac.serverAddr)
		for err != nil {

			tcpClient, err = net.DialTCP("tcp", nil, kac.serverAddr)
			time.Sleep(kac.echoInterval)
			fmt.Println("正在重新拨号...", err)
		}
		kac.TCPConn = tcpClient
		kac.lastEchoTime = time.Now()
	}
	return true
}
func (kac *KaConn) StartEchoServer() {

	b := make([]byte, HBLEGHT)
	for {
		//		select {
		//case <-time.Now()-
		//		}
		//这里最终要采用 select 时间堵塞的方式来完善，要不循环跑到太快了耗资源。
		if kac.TCPConn == nil {
			kac.reGetTcpConn()
		}
		if kac.lastEchoTime.Add(kac.echoInterval).Before(time.Now()) {
			kac.TCPConn.Close()
			kac.TCPConn = nil
		}

		kac.TCPConn.SetDeadline(kac.lastEchoTime.Add(kac.connTimeOut))
		kac.Lock()
		nn := 0
		var err error
		if kac.TCPConn != nil {
			nn, err = kac.TCPConn.Read(b)
		}
		kac.Unlock()
		kac.TCPConn.SetDeadline(time.Time{})

		//如果连接有问题，则重新申请一条命令通道
		if err != nil {
			kac.TCPConn.Close()
			kac.TCPConn = nil
			fmt.Println("kaTcp.StartEchoServer->kac.TCPConn.Read,连接关闭了！", err, kac.connTimeOut.Seconds())
			kac.reGetTcpConn()
			continue
		}
		fmt.Println("kaTcp.StartEchoServer->kac.TCPConn.Read我刚刚执行完了一次tcp原生read函数，读取的数据是：", b[:nn])
		kac.lastEchoTime = time.Now()

		if bytes.Equal(b[:nn], kac.echoData[:]) {
			//fmt.Println(kac.TCPConn, kac.lastEchoTime, kac.echoInterval, "katcp")
			//如果读到了心跳包，那么更新过期时间时间，并回复应答包
			fmt.Println("回复一个心跳包", kac.replyData)
			kac.TCPConn.Write(kac.replyData[:])

		} else {
			//如果读到的不是心跳包，并把误读的业务数据写到误读缓冲区，并更新过期时间
			for i := 0; i < nn; i++ {
				kac.buf <- b[i] //如果业务数据长时间不被拿走，这里可能会堵塞，造成心跳超时，通道被废止,对缓冲区满有什么好的处理方案？
			}
		}

	}
}
func (kac *KaConn) StartEchoClient() {
	//  拨号到互联网上的透传服务器,拨不通，就打出提示消息并一直拨

	b := make([]byte, HBLEGHT)
	ticker := time.NewTicker(kac.echoInterval)
	defer ticker.Stop()
	for range ticker.C {
		//		select {
		//case <-time.Now()-
		//		}
		//这里最终要采用 select 时间堵塞的方式来完善，要不循环跑到太快了耗资源。

		//如果最后读写数据时间+心跳间隔时间小于现在的时间，则要表示需要发送心跳包了
		if kac.TCPConn == nil {
			kac.reDailTcpConn()
		}

		//心跳时间到，客户端向服务端发送一个心跳包
		kac.Write(kac.echoData[:])

		//读心跳包的时候，先从心跳误读缓冲区读
		n := 0
		exitSelectFlag := false
		for {
			select {
			case b[n] = <-kac.echoBuf:
				n++
				if n == HBLEGHT {
					exitSelectFlag = true
				}
			default:
				exitSelectFlag = true
			}
			if exitSelectFlag {
				break
			}
		}
		//n==0表示心跳误读缓冲区中无数据，需要从tcpconn中直接读心跳数据
		if n == 0 {
			kac.TCPConn.SetDeadline(kac.lastEchoTime.Add(kac.connTimeOut))
			kac.Lock()
			nn := 0
			var err error
			if kac.TCPConn != nil {
				nn, err = kac.TCPConn.Read(b)
			}
			kac.Unlock()
			kac.TCPConn.SetDeadline(time.Time{})

			if err != nil {
				kac.TCPConn.Close()
				kac.TCPConn = nil
				//kac = nil
				fmt.Println("kacTcp.StartEchoClient->kac.TCPConn.Read ,连接关闭了！正在重新建立命令通道", err)
				kac.reDailTcpConn()
				continue
			}
			//如果读到业务，不管是不是心跳数据，则证明连接是活的，更新最新读写时间,并更新连接的过期时间
			kac.lastEchoTime = time.Now()

			//如果读到的是心跳应答数据包，则什么也不做，如果读到的是业务数据，则到误读的业务数据保存到误读缓冲区
			if bytes.Equal(b[:nn], kac.replyData[:]) {
				fmt.Println("收到一个心跳回复", b[:nn])
			} else {
				for i := 0; i < nn; i++ {
					kac.buf <- b[i] //如果业务数据长时间不被拿走，这里可能会堵塞，造成心跳超时，通道被废止,对缓冲区满有什么好的处理方案？
				}
			}
		} else {
			kac.lastEchoTime = time.Now()
		}
	}
}

//
func (kac *KaConn) Read(b []byte) (int, error) {
	n := 0
	for {
		select {
		case b[n] = <-kac.buf:
			n++
			//如果误读缓冲区内的数据已经填满了业务数据的读缓冲，则直接返回已读数据
			if n == len(b) {
				return n, nil
			}
		default:
			//执行到这里证明误读缓冲区的数据已经读完，业务缓冲还没有填满，那么需要再从系统缓冲中读数据到业务缓冲里去。
			if kac.TCPConn != nil {

				kac.Lock()
				nn := 0
				var err error
				if kac.TCPConn != nil {
					nn, err = kac.TCPConn.Read(b[n:])
				}
				kac.Unlock()

				if err != nil {

					fmt.Println("kaTcp.Read->kac.TCPConn.Read,tcp原生读数据出错", err, kac.connTimeOut.Seconds())
					time.Sleep(kac.echoInterval)
					return 0, err
				}
				kac.lastEchoTime = time.Now()
				//如果是误读了心跳数据，那么把误读的心跳数据写入心跳误读缓冲区后继续读(进行读阻塞)
				if bytes.Equal(b[n:], kac.echoData[:]) || bytes.Equal(b[n:], kac.replyData[:]) {
					for i := 0; i < HBLEGHT; i++ {
						kac.echoBuf <- b[n+i]
					}
					if n == 0 {
						continue
					}
				}
				//正常的业务数据读取为啥要管连接存活？？？可以不管！
				fmt.Println("katcp.Read()函数", "||||", kac.TCPConn)
				return n + nn, err
			} else {
				//如果tcp连接为空，则重连tcp连接，然后再读，问题是，这里如何得到tcp连接?
				//所以这里是无法产生tcp连接的，因为不知道是服务端还是客户端在调用这个read函数
				//只能等心跳函数去重建连接,如果要较发的解决这个问题，还是只有把kaConn的客户端包和服务端包分成两个对象来写
				//这里已经模拟出读阻塞了
				//	time.Sleep(time.Second * 2)

				//return 0, errors.New("no tcp conn")
			}
		}
	}
}
func (kac *KaConn) Write(b []byte) (int, error) {
	//写的时候要先计时再写，防止写的时候卡住了
	for {
		if kac.TCPConn != nil {

			kac.TCPConn.SetDeadline(kac.lastEchoTime.Add(kac.connTimeOut))
			n, err := kac.TCPConn.Write(b)
			kac.TCPConn.SetDeadline(time.Time{})
			if err != nil {
				kac.lastEchoTime = time.Now()
			}
			return n, err
		} else {
			time.Sleep(time.Second * 2)
		}
	}
}
func clearBytesSlice(b []byte) {
	for i := 0; i < len(b); i++ {
		b[i] = 0x00
	}
}
