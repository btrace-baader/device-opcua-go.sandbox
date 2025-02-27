package test

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/monitor"
	"github.com/gopcua/opcua/ua"
)

const (
	currentTimeNodeID   = "ns=0;i=2258"
	disconnectTimeout   = 5 * time.Second
	reconnectionTimeout = 10 * time.Second
)

func createMethodRequest(objectId string, methodId string) *ua.CallMethodRequest {
	method := &ua.CallMethodRequest{
		ObjectID:       ua.NewStringNodeID(2, objectId),
		MethodID:       ua.NewStringNodeID(2, methodId),
		InputArguments: []*ua.Variant{},
	}
	return method
}

// TestAutoReconnection performs an integration test the auto reconnection
// from an OPC/UA server.
func TestAutoReconnection(t *testing.T) {
	ctx := context.Background()

	server := NewServer("reconnection_server.py")
	defer closeServer(server)

	c := opcua.NewClient(server.Endpoint, server.Opts...)
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.CloseWithContext(ctx)

	m, err := monitor.NewNodeMonitor(c)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tests := []struct {
		name string
		req  *ua.CallMethodRequest
	}{
		{
			name: "connection_failure",
			req: &ua.CallMethodRequest{
				ObjectID:       ua.NewStringNodeID(2, "simulations"),
				MethodID:       ua.NewStringNodeID(2, "simulate_connection_failure"),
				InputArguments: []*ua.Variant{},
			},
		},
		{
			name: "securechannel_failure",
			req:  createMethodRequest("simulations", "simulate_securechannel_failure"),
		},
		{
			name: "session_failure",
			req:  createMethodRequest("simulations", "simulate_session_failure"),
		},
		{
			name: "subscription_failure",
			req:  createMethodRequest("simulations", "simulate_subscription_failure"),
		},
	}

	ch := make(chan *monitor.DataChangeMessage, 5)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sub, err := m.ChanSubscribe(
		sctx,
		&opcua.SubscriptionParameters{Interval: opcua.DefaultSubscriptionInterval},
		ch,
		currentTimeNodeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe(ctx)

	for _, tt := range tests {
		ok := t.Run(tt.name, func(t *testing.T) {

			if msg := <-ch; msg.Error != nil {
				t.Fatalf("No error expected for first value: %s", msg.Error)
			}

			downC := make(chan struct{}, 1)
			dTimeout := time.NewTimer(disconnectTimeout)
			go c.CallWithContext(ctx, tt.req)

			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				// make sure the connection is down
				for {
					select {
					case <-ctx.Done():
						return
					default:
						if c.State() != opcua.Connected {
							downC <- struct{}{}
							return
						}
						// HACK: scanning the state of client to determine if the connection has failed
						// is not good pratice, as with powerful machine the reconnection could be faster
						// then 1 ms and it will not detect the change, a solution could be a state event
						// or a reconnection counter
						time.Sleep(1 * time.Millisecond)
					}
				}
			}()

			select {
			case <-dTimeout.C:
				cancel()
				t.Fatal("Timeout reached, the connection did not go down as expected")
			case <-downC:
			}

			// empty out the channel
			for len(ch) > 0 {
				<-ch
			}

			rTimeout := time.NewTimer(reconnectionTimeout)
			select {
			case <-rTimeout.C:
				t.Fatal("Timeout reached, reconnection failed")
			case msg := <-ch:
				if err := msg.Error; err != nil {
					t.Fatal(err)
				}
			}
		})
		if !ok {
			t.FailNow()
		}
	}
}
