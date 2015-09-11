// DNS to GRPC.

package dnstogrpc

import (
	"fmt"
	"sync"
	"time"

	pb "blitiri.com.ar/go/dnss/proto"
	"blitiri.com.ar/go/dnss/util"
	"github.com/golang/glog"
	"github.com/miekg/dns"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type grpcclient struct {
	Upstream string
	CAFile   string
	client   pb.DNSServiceClient
}

func (c *grpcclient) Connect() error {
	var err error
	var creds credentials.TransportAuthenticator
	if c.CAFile == "" {
		creds = credentials.NewClientTLSFromCert(nil, "")
	} else {
		creds, err = credentials.NewClientTLSFromFile(c.CAFile, "")
		if err != nil {
			return err
		}
	}

	conn, err := grpc.Dial(c.Upstream, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}

	c.client = pb.NewDNSServiceClient(conn)
	return nil
}

func (c *grpcclient) Query(r *dns.Msg) (*dns.Msg, error) {
	buf, err := r.Pack()
	if err != nil {
		return nil, err
	}

	// Give our RPCs 2 second timeouts: DNS usually doesn't wait that long
	// anyway.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	g, err := c.client.Query(ctx, &pb.RawMsg{Data: buf})
	if err != nil {
		return nil, err
	}

	m := &dns.Msg{}
	err = m.Unpack(g.Data)
	return m, err
}

type Server struct {
	Addr string

	client *grpcclient
}

func New(addr, upstream, caFile string) *Server {
	return &Server{
		Addr: addr,
		client: &grpcclient{
			Upstream: upstream,
			CAFile:   caFile,
		},
	}
}

func (s *Server) Handler(w dns.ResponseWriter, r *dns.Msg) {
	prefix := ""
	if glog.V(3) {
		prefix = fmt.Sprintf("%v %v", w.RemoteAddr(), r.Id)
		glog.Infof("DNS  %v %v", prefix, util.QuestionsToString(r.Question))
	}

	// TODO: we should create our own IDs, in case different users pick the
	// same id and we pass that upstream.

	from_up, err := s.client.Query(r)
	if err != nil {
		glog.V(3).Infof("DNS  %v  ERR: %v", prefix, err)
	}

	if from_up != nil {
		if from_up.Rcode != dns.RcodeSuccess {
			rcode := dns.RcodeToString[from_up.Rcode]
			glog.V(3).Infof("DNS  %v  !->  %v", prefix, rcode)
		}
		for _, rr := range from_up.Answer {
			glog.V(3).Infof("DNS  %v  ->  %v", prefix, rr)
		}
		w.WriteMsg(from_up)
	}
}

func (s *Server) ListenAndServe() {
	err := s.client.Connect()
	if err != nil {
		glog.Errorf("Error creating GRPC client: %v", err)
		return
	}

	glog.Infof("DNS listening on %s", s.Addr)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "udp", dns.HandlerFunc(s.Handler))
		glog.Errorf("Exiting UDP: %v", err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "tcp", dns.HandlerFunc(s.Handler))
		glog.Errorf("Exiting TCP: %v", err)
	}()

	wg.Wait()
}
