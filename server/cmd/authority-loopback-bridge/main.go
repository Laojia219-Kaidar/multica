package main
import("io";"log";"net";"os";"strings";"time")
const bridgePort="3151"
const targetAddr="127.0.0.1:3150"
const maxConnections=32
func main(){bind:=strings.TrimSpace(os.Getenv("HIVECOSM_AUTHORITY_BRIDGE_BIND_ADDR"));ip:=net.ParseIP(bind);if ip==nil||ip.To4()==nil||ip.IsLoopback()||ip.IsUnspecified()||ip.IsMulticast(){log.Fatal("bridge bind address must be explicit non-loopback IPv4")};l,e:=net.Listen("tcp4",net.JoinHostPort(bind,bridgePort));if e!=nil{log.Fatal(e)};defer l.Close();sem:=make(chan struct{},maxConnections);for{c,e:=l.Accept();if e!=nil{log.Fatal(e)};select{case sem<-struct{}{}:go func(){defer func(){<-sem}();proxy(c)}();default:_=c.Close()}}}
func proxy(c net.Conn){defer c.Close();_=c.SetDeadline(time.Now().Add(15*time.Second));u,e:=net.DialTimeout("tcp4",targetAddr,3*time.Second);if e!=nil{return};defer u.Close();_=u.SetDeadline(time.Now().Add(15*time.Second));d:=make(chan struct{},2);go func(){_,_=io.Copy(u,c);if x,ok:=u.(*net.TCPConn);ok{_=x.CloseWrite()};d<-struct{}{}}();go func(){_,_=io.Copy(c,u);if x,ok:=c.(*net.TCPConn);ok{_=x.CloseWrite()};d<-struct{}{}}();<-d}
