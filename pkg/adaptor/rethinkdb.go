package adaptor

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/compose/transporter/pkg/message"
	"github.com/compose/transporter/pkg/pipe"
	gorethink "github.com/dancannon/gorethink"
)

// Rethinkdb is an adaptor that writes metrics to rethinkdb (http://rethinkdb.com/)
// An open-source distributed database
type Rethinkdb struct {
	// pull these in from the config
	uri *url.URL

	// save time by setting these once
	database string
	table    string

	debug bool

	//
	pipe *pipe.Pipe
	path string

	// rethinkdb connection and options
	client *gorethink.Session
}

// NewRethinkdb creates a new Rethinkdb database adaptor
func NewRethinkdb(p *pipe.Pipe, path string, extra Config) (StopStartListener, error) {
	var (
		conf dbConfig
		err  error
	)
	if err = extra.Construct(&conf); err != nil {
		return nil, err
	}

	u, err := url.Parse(conf.URI)
	if err != nil {
		return nil, err
	}

	r := &Rethinkdb{
		uri:  u,
		pipe: p,
		path: path,
	}

	r.database, r.table, err = extra.splitNamespace()
	if err != nil {
		return r, err
	}
	r.debug = conf.Debug

	return r, nil
}

// Start the adaptor as a source (not implemented)
func (r *Rethinkdb) Start() error {
	return fmt.Errorf("rethinkdb can't function as a source")
}

// Listen start's the adaptor's listener
func (r *Rethinkdb) Listen() (err error) {
	r.client, err = r.setupClient()
	if err != nil {
		r.pipe.Err <- err
		return err
	}

	return r.pipe.Listen(r.applyOp)
}

// Stop the adaptor
func (r *Rethinkdb) Stop() error {
	r.pipe.Stop()
	return nil
}

// applyOp applies one operation to the database
func (r *Rethinkdb) applyOp(msg *message.Msg) (*message.Msg, error) {
	var (
		resp gorethink.WriteResponse
		err  error
	)

	if !msg.IsMap() {
		r.pipe.Err <- NewError(ERROR, r.path, "rethinkdb error (document must be a json document)", msg.Data)
		return msg, nil
	}
	doc := msg.Map()

	switch msg.Op {
	case message.Delete:
		id, err := msg.IDString("id")
		if err != nil {
			r.pipe.Err <- NewError(ERROR, r.path, "rethinkdb error (cannot delete an object with a nil id)", msg.Data)
			return msg, nil
		}
		resp, err = gorethink.Table(r.table).Get(id).Delete().RunWrite(r.client)
	case message.Insert:
		resp, err = gorethink.Table(r.table).Insert(doc).RunWrite(r.client)
	case message.Update:
		resp, err = gorethink.Table(r.table).Insert(doc, gorethink.InsertOpts{Conflict: "replace"}).RunWrite(r.client)
	}
	if err != nil {
		r.pipe.Err <- NewError(ERROR, r.path, "rethinkdb error (%s)", err)
		return msg, nil
	}

	err = r.handleResponse(&resp)
	if err != nil {
		r.pipe.Err <- NewError(ERROR, r.path, "rethinkdb error (%s)", err)
	}

	return msg, nil
}

func (r *Rethinkdb) setupClient() (*gorethink.Session, error) {
	// set up the clientConfig, we need host:port, username, password, and database name
	if r.debug {
		fmt.Printf("Connecting to %s\n", r.uri.Host)
	}
	client, err := gorethink.Connect(gorethink.ConnectOpts{
		Address:     r.uri.Host,
		MaxIdle:     10,
		IdleTimeout: time.Second * 10,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to connect: %s", err)
	}

	if r.debug {
		fmt.Printf("dropping and creating table '%s' on database '%s'\n", r.table, r.database)
	}
	gorethink.Db(r.database).TableDrop(r.table).RunWrite(client)
	gorethink.Db(r.database).TableCreate(r.table).RunWrite(client)

	client.Use(r.database)
	return client, nil
}

// handleresponse takes the rethink response and turn it into something we can consume elsewhere
func (r *Rethinkdb) handleResponse(resp *gorethink.WriteResponse) error {
	if resp.Errors != 0 {
		if !strings.Contains(resp.FirstError, "Duplicate primary key") { // we don't care about this error
			if r.debug {
				fmt.Printf("Reported %d errors\n", resp.Errors)
			}
			return fmt.Errorf("%s\n%s", "problem inserting docs", resp.FirstError)
		}
	}
	return nil
}
