package main

import (
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/johnnylee/goutil/logutil"
)

var log logutil.Logger

type Config struct {
	// userCmd is appended to the ssh command line. We additionally add
	// commands to make a forwarding loop to monitor the connection.
	userCmd string

	// Wait after the ssh connection attempt before beginning to monitor the
	// connection.
	connectWait time.Duration

	pingInterval time.Duration // Time between pings.
	pingTimeout  time.Duration // Fail timeout for ping loop.
	retryWait    time.Duration //

	pingClientPort int
	pingServerPort int

	pingListener net.Listener // Server
	pingConn     net.Conn     // Client
	pingChan     chan byte
	cmdStr       string
	cmd          *exec.Cmd
}

type StateFunc func(*Config) StateFunc

func runSshCommand(conf *Config) StateFunc {
	conf.pingClientPort = 32768 + rand.Intn(28233)
	conf.pingServerPort = 32768 + rand.Intn(28233)

	conf.cmdStr = "ssh " +
		"-o ControlPersist=no -o ControlMaster=no -o GatewayPorts=yes " +
		"-N -L " +
		fmt.Sprintf("%v:localhost:%v -R %v:localhost:%v ",
			conf.pingClientPort,
			conf.pingClientPort,
			conf.pingClientPort,
			conf.pingServerPort) +
		conf.userCmd

	log.Msg("Running command: %v", conf.cmdStr)
	conf.cmd = exec.Command("bash", "-i", "-c", conf.cmdStr)

	go func() {
		output, err := conf.cmd.CombinedOutput()
		log.Msg("SSH command output: %v", string(output))
		if err != nil {
			log.Err(err, "When executing SSH command")
		}
	}()

	return startPingServer
}

func sleepRetry(conf *Config) StateFunc {
	log.Msg("Sleeping before retrying...")
	conf.cmd.Process.Kill()
	if conf.pingConn != nil {
		conf.pingConn.Close()
		conf.pingConn = nil
	}
	if conf.pingListener != nil {
		conf.pingListener.Close()
		conf.pingListener = nil
	}
	time.Sleep(conf.retryWait)
	return runSshCommand
}

func runPingServer(l net.Listener, pingChan chan byte) {
	conn, err := l.Accept()
	if err != nil {
		log.Err(err, "When accepting ping connection")
		return
	}

	buf := make([]byte, 1)

	for {
		_, err = conn.Read(buf)
		if err != nil {
			log.Err(err, "When reading from ping connection")
			return
		}

		select {
		case pingChan <- buf[0]:

		default:
			log.Msg("Ping channel full. Stopping ping server.")
			return
		}
	}
}

func startPingServer(conf *Config) StateFunc {
	addr := fmt.Sprintf("localhost:%v", conf.pingServerPort)
	log.Msg("Starting ping server on: %v", addr)

	var err error
	conf.pingListener, err = net.Listen("tcp", addr)
	if err != nil {
		log.Err(err, "When creating server listener")
		return sleepRetry
	}

	go runPingServer(conf.pingListener, conf.pingChan)

	time.Sleep(conf.connectWait)

	return startPingClient
}

func runPingClient(conn net.Conn, pingTimeout, pingInterval time.Duration) {
	// Send pings.
	for {
		// Set timeout.
		err := conn.SetWriteDeadline(time.Now().Add(pingTimeout))
		if err != nil {
			log.Err(err, "When setting ping client write deadline")
			return
		}

		// Write ping data.
		if _, err = conn.Write([]byte("1")); err != nil {
			log.Err(err, "When writing ping data")
			return
		}

		time.Sleep(pingInterval)
	}
}

func startPingClient(conf *Config) StateFunc {
	addr := fmt.Sprintf("localhost:%v", conf.pingClientPort)
	log.Msg("Starting ping client on: %v", addr)

	var err error
	conf.pingConn, err = net.DialTimeout("tcp", addr, conf.pingInterval)
	if err != nil {
		log.Err(err, "When dialing ping client port")
		return sleepRetry
	}

	go runPingClient(conf.pingConn, conf.pingTimeout, conf.pingInterval)

	return pingLoop
}

func pingLoop(conf *Config) StateFunc {
	for {
		select {
		case <-conf.pingChan:
			log.Msg("Ping")
		case <-time.After(conf.pingTimeout):
			log.Msg("Timed out waiting for ping.")
			return sleepRetry
		}
	}
}

func main() {
	rand.Seed(time.Now().UnixNano())
	conf := Config{}
	conf.userCmd = strings.Join(os.Args[1:], " ")
	log = logutil.New("AutoSSH: " + conf.userCmd)
	conf.connectWait = 8 * time.Second
	conf.pingInterval = 8 * time.Second
	conf.pingTimeout = 32 * time.Second
	conf.retryWait = 32 * time.Second
	conf.pingChan = make(chan byte)
	fn := runSshCommand(&conf)
	for {
		fn = fn(&conf)
	}
}
