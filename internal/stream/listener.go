package stream

import "net"

// defaultListen is the ONLY place this package creates a listener.
//
// Every listener goes through Config.listen, which defaults to this. That
// indirection is what makes "no TCP listener exists" a claim a test can settle
// by recording what the factory was asked for, rather than by scanning /proc
// and hoping the sample caught everything. TestEveryListenerGoesThroughTheFactory
// pins that net.Listen is named here and nowhere else in the package.
func defaultListen(network, addr string) (net.Listener, error) {
	return net.Listen(network, addr)
}
