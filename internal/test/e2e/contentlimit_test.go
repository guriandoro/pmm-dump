package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"pmm-dump/internal/test/util"
	"pmm-dump/pkg/dump"
	"pmm-dump/pkg/victoriametrics"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"
)

func TestContentLimit(t *testing.T) {
	pmm := util.NewPMM(t, "content-limit", ".env.test")
	if pmm.UseExistingDeployment() {
		t.Skip("skipping test because existing deployment is used")
	}
	pmm.Stop()

	ctx := context.Background()

	b := new(util.Binary)
	tmpDir := util.TestDir(t, "content-limit-test")
	dumpPath := filepath.Join(tmpDir, "dump.tar.gz")
	err := generateFakeDump(dumpPath)
	if err != nil {
		t.Fatal(err)
	}

	pmm.Deploy()
	defer pmm.Stop()

	stdout, stderr, err := util.Exec(ctx, "", "docker", "compose", "exec", "pmm-server", "bash", "-c", "sed -i -e 's/client_max_body_size 10m/client_max_body_size 1m/g' /etc/nginx/conf.d/pmm.conf")
	if err != nil {
		t.Fatal("failed to change nginx settings", err, stdout, stderr)
	}

	pmm.Restart()

	stdout, stderr, err = b.Run(
		"import",
		"-d", dumpPath,
		"--pmm-url", pmm.PMMURL(),
	)
	if err != nil {
		if !strings.Contains(stderr, "413 Request Entity Too Large") {
			t.Fatal("expected `413 Request Entity Too Large` error, got", err, stdout, stderr)
		}
	} else {
		t.Fatal("expected `413 Request Entity Too Large` error but import didn't fail")
	}

	t.Log("Importing with 10KB limit")

	stdout, stderr, err = b.Run(
		"import",
		"-d", dumpPath,
		"--pmm-url", pmm.PMMURL(),
		"--vm-content-limit", "10024",
	)
	if err != nil {
		t.Fatal("failed to import", err, stdout, stderr)
	}
}

func generateFakeDump(filepath string) error {
	file, err := os.Create(filepath)
	if err != nil {
		return errors.Wrap(err, "failed to open file")
	}
	defer file.Close()
	gzw, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return errors.Wrap(err, "failed to create gzip writer")
	}
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	meta := &dump.Meta{
		VMDataFormat: "json",
	}

	metaContent, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal dump meta: %s", err)
	}

	err = tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     dump.MetaFilename,
		Size:     int64(len(metaContent)),
		Mode:     0600,
		ModTime:  time.Now(),
	})
	if err != nil {
		return errors.Wrap(err, "failed to write dump meta")
	}

	if _, err = tw.Write(metaContent); err != nil {
		return errors.Wrap(err, "failed to write dump meta content")
	}

	for i := 0; i < 10; i++ {
		content, err := generateFakeChunk(100000)
		if err != nil {
			return errors.Wrap(err, "failed to generate fake chunk")
		}

		chunkSize := int64(len(content))

		err = tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     path.Join("vm", fmt.Sprintf("chunk-%d.bin", i)),
			Size:     chunkSize,
			Mode:     0600,
			ModTime:  time.Now(),
			Uid:      1,
		})
		if err != nil {
			return errors.Wrap(err, "failed to write file header")
		}
		if _, err = tw.Write(content); err != nil {
			return errors.Wrap(err, "failed to write chunk content")
		}
	}
	return nil
}

func generateFakeChunk(size int) ([]byte, error) {
	r := rand.New(rand.NewSource(time.Now().Unix()))
	data := []byte{}
	for i := 0; i < size; i++ {
		metricsData, err := json.Marshal(victoriametrics.Metric{
			Metric: map[string]string{
				"__name__": "test",
				"job":      "test",
				"instance": "test-" + strconv.Itoa(i),
				"test":     strconv.Itoa(int(time.Now().UnixNano())),
			},
			Values:     []float64{r.NormFloat64()},
			Timestamps: []int64{time.Now().UnixNano()},
		})
		if err != nil {
			return nil, errors.Wrap(err, "marshal metrics")
		}
		data = append(data, metricsData...)
	}
	return compressData(data)
}

func compressData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		return nil, errors.Wrap(err, "write gzip")
	}
	if err := gw.Close(); err != nil {
		return nil, errors.Wrap(err, "close gzip")
	}
	return buf.Bytes(), nil
}
