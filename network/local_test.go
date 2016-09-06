package network

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/stretchr/testify/assert"
)

func TestLocalListener(t *testing.T) {
	addr := NewLocalAddress("127.0.0.1:2000")
	listener, err := NewLocalListener(addr)
	if err != nil {
		t.Fatal(err)
	}

	var ready = make(chan bool)
	go func() {
		ready <- true
		err := listener.Listen(func(c Conn) {})
		if err != nil {
			t.Error("Should not have had error while listening")
		}
		ready <- true
	}()

	<-ready
	// give it some time
	time.Sleep(20 * time.Millisecond)
	if err := listener.Listen(func(c Conn) {}); err == nil {
		t.Error("listener should have returned an error when Listen twice")
	}
	assert.Nil(t, listener.Stop())
	if err := listener.Stop(); err == nil {
		t.Error("listener.Stop() twice should have returned an error")
	}
	<-ready
}

// Test whether a call to a conn.Close() will stop the remote Receive() call
func TestLocalConnCloseReceive(t *testing.T) {
	addr := NewLocalAddress("127.0.0.1:2000")
	listener, err := NewLocalListener(addr)
	if err != nil {
		t.Fatal("Could not listen", err)
	}

	var ready = make(chan bool)
	go func() {
		ready <- true
		listener.Listen(func(c Conn) {
			ready <- true
			assert.Nil(t, c.Close())
		})
	}()
	<-ready

	outgoing, err := NewLocalConn(addr, addr)
	if err != nil {
		t.Fatal("erro NewLocalConn:", err)
	}
	<-ready

	_, err = outgoing.Receive(context.TODO())
	assert.Equal(t, ErrClosed, err)
	assert.Equal(t, ErrClosed, outgoing.Close())
	assert.Nil(t, listener.Stop())

}

// Test if we can run two parallel local network using two different contexts
func TestLocalContext(t *testing.T) {
	ctx1 := NewLocalManager()
	ctx2 := NewLocalManager()

	addrListener := NewLocalAddress("127.0.0.1:2000")
	addrConn := NewLocalAddress("127.0.0.1:2001")

	done1 := make(chan error)
	done2 := make(chan error)

	go testConnListener(ctx1, done1, addrListener, addrConn, 1)
	go testConnListener(ctx2, done2, addrListener, addrConn, 2)

	var confirmed int
	for confirmed != 2 {
		var err error
		select {
		case err = <-done1:
		case err = <-done2:
		}

		if err != nil {
			t.Fatal(err)
		}
		confirmed++
	}
}

// launch a listener, then a Conn and communicate their own address + individual
// val
func testConnListener(ctx *LocalManager, done chan error, listenA, connA Address, secret int) {
	listener, err := NewLocalListenerWithManager(ctx, listenA)
	if err != nil {
		done <- err
		return
	}

	var ok = make(chan error)

	// make the listener send and receive a struct that only they can know (this
	// listener + conn
	handshake := func(c Conn, sending, receiving Address) error {
		err := c.Send(context.TODO(), &AddressTest{sending, secret})
		if err != nil {
			return err
		}
		p, err := c.Receive(context.TODO())
		if err != nil {
			return err
		}

		at := p.Msg.(AddressTest)
		if at.Addr != receiving {
			return fmt.Errorf("Receiveid wrong address")
		}
		if at.Val != secret {
			return fmt.Errorf("Received wrong secret")
		}
		return nil
	}

	go func() {
		ok <- nil
		listener.Listen(func(c Conn) {
			ok <- nil
			err := handshake(c, listenA, connA)
			ok <- err
		})
		ok <- nil
	}()
	// wait go routine to start
	<-ok

	// trick to use host because it already tries multiple times to connect if
	// the listening routine is not up yet.
	h, err := NewLocalHostWithManager(ctx, connA)
	if err != nil {
		done <- err
		return
	}
	c, err := h.Connect(listenA)
	if err != nil {
		done <- err
		return
	}

	// wait listening function to start for the incoming conn
	<-ok
	err = handshake(c, connA, listenA)
	if err != nil {
		done <- err
		return
	}
	// wait for any err of the handshake from the listening PoV
	err = <-ok
	if err != nil {
		done <- err
		return
	}
	if err := c.Close(); err != nil {
		done <- err
		return
	}

	listener.Stop()
	<-ok
	done <- nil
}

func TestLocalConnDiffAddress(t *testing.T) {
	testLocalConn(t, NewLocalAddress("127.0.0.1:2000"), NewLocalAddress("127.0.0.1:2001"))
}

func TestLocalConnSameAddress(t *testing.T) {
	testLocalConn(t, NewLocalAddress("127.0.0.1:2000"), NewLocalAddress("127.0.0.1:2000"))
}

func testLocalConn(t *testing.T, a1, a2 Address) {
	addr1 := a1
	addr2 := a2

	listener, err := NewLocalListener(addr1)
	if err != nil {
		t.Fatal("Could not listen", err)
	}

	var ready = make(chan bool)
	var incomingConn = make(chan bool)
	var outgoingConn = make(chan bool)
	go func() {
		ready <- true
		listener.Listen(func(c Conn) {
			incomingConn <- true
			nm, err := c.Receive(context.TODO())
			assert.Nil(t, err)
			assert.Equal(t, 3, nm.Msg.(SimpleMessage).I)
			// acknoledge the message
			incomingConn <- true
			err = c.Send(context.TODO(), &SimpleMessage{3})
			assert.Nil(t, err)
			//wait ack
			<-outgoingConn
			// close connection
			assert.Nil(t, c.Close())
		})
		ready <- true
	}()
	<-ready

	outgoing, err := NewLocalConn(addr2, addr1)
	if err != nil {
		t.Fatal("erro NewLocalConn:", err)
	}

	// check if connection is opened on the listener
	<-incomingConn
	// send stg and wait for ack
	assert.Nil(t, outgoing.Send(context.TODO(), &SimpleMessage{3}))
	<-incomingConn

	// receive stg and send ack
	nm, err := outgoing.Receive(context.TODO())
	assert.Nil(t, err)
	assert.Equal(t, 3, nm.Msg.(SimpleMessage).I)
	outgoingConn <- true

	// close the incoming conn, so Receive here should return an error
	nm, err = outgoing.Receive(context.TODO())
	if err != ErrClosed {
		t.Error("Receive should have returned an error")
	}
	assert.Equal(t, ErrClosed, outgoing.Close())

	// close the listener
	assert.Nil(t, listener.Stop())
	<-ready
}

func TestLocalManyConn(t *testing.T) {
	nbrConn := 3
	addr := NewLocalAddress("127.0.0.1:2000")
	listener, err := NewLocalListener(addr)
	if err != nil {
		t.Fatal("Could not setup listener:", err)
	}
	var wg sync.WaitGroup
	go func() {
		listener.Listen(func(c Conn) {
			_, err := c.Receive(context.TODO())
			assert.Nil(t, err)

			assert.Nil(t, c.Send(context.TODO(), &SimpleMessage{3}))
		})
	}()

	if !waitListeningUp(addr) {
		t.Fatal("Can't get listener up")
	}
	wg.Add(nbrConn)
	for i := 1; i <= nbrConn; i++ {
		go func(j int) {
			a := NewLocalAddress("127.0.0.1:" + strconv.Itoa(2000+j))
			c, err := NewLocalConn(a, addr)
			if err != nil {
				t.Fatal(err)
			}
			assert.Nil(t, c.Send(context.TODO(), &SimpleMessage{3}))
			nm, err := c.Receive(context.TODO())
			assert.Nil(t, err)
			assert.Equal(t, 3, nm.Msg.(SimpleMessage).I)
			assert.Nil(t, c.Close())
			wg.Done()
		}(i)
	}

	wg.Wait()
	listener.Stop()
}

func waitListeningUp(addr Address) bool {
	for i := 0; i < 5; i++ {
		if defaultLocalManager.isListening(addr) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func NewTestLocalHost(port int) (*LocalHost, error) {
	addr := NewLocalAddress("127.0.0.1:" + strconv.Itoa(port))
	return NewLocalHost(addr)
}

type AddressTest struct {
	Addr Address
	Val  int
}

var AddressTestType = RegisterPacketType(&AddressTest{})
