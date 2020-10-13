package tests

import (
	"os"
	"os/signal"
	"testing"
	"time"

	"github.com/spiral/endure"
	"github.com/stretchr/testify/assert"
	"github.com/temporalio/roadrunner-temporal/config"
)

func TestViperProvider_Init(t *testing.T) {
	container, err := endure.NewContainer(endure.DebugLevel, endure.RetryOnFail(true))
	if err != nil {
		t.Fatal(err)
	}
	vp := &config.ViperProvider{}
	vp.Path = ".rr.yaml"
	vp.Prefix = "rr"
	err = container.Register(vp)
	if err != nil {
		t.Fatal(err)
	}

	err = container.Register(&Foo{})
	if err != nil {
		t.Fatal(err)
	}

	err = container.Init()
	if err != nil {
		t.Fatal(err)
	}

	errCh, err := container.Serve()
	if err != nil {
		t.Fatal(err)
	}

	// stop by CTRL+C
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt)

	tt := time.NewTicker(time.Second * 2)

	for {
		select {
		case e := <-errCh:
			assert.NoError(t, e.Error.Err)
			assert.NoError(t, container.Stop())
			return
		case <-c:
			er := container.Stop()
			if er != nil {
				panic(er)
			}
			return
		case <-tt.C:
			tt.Stop()
			assert.NoError(t, container.Stop())
			return
		}
	}

}
