package dockerapp

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
)

// Follow streams a container's log lines to fn until ctx is canceled or the
// stream ends. Lines are delivered in small batches.
func (c *Client) Follow(ctx context.Context, id string, tail int, fn func(lines []string)) error {
	if tail <= 0 {
		tail = 200
	}
	path := "http://docker/" + apiVersion + "/containers/" + url.PathEscape(id) +
		"/logs?stdout=1&stderr=1&follow=1&tail=" + strconv.Itoa(tail)
	req, err := newRequestWithContext(ctx, path)
	if err != nil {
		return err
	}
	resp, err := c.httpStream.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	first, err := br.Peek(1)
	if err != nil {
		return nil
	}
	if first[0] > 2 { // raw TTY stream
		sc := bufio.NewScanner(br)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			fn([]string{sc.Text()})
			if ctx.Err() != nil {
				return nil
			}
		}
		return nil
	}
	// Multiplexed stream: 8-byte frame headers.
	var pending strings.Builder
	hdr := make([]byte, 8)
	for ctx.Err() == nil {
		if _, err := io.ReadFull(br, hdr); err != nil {
			return nil
		}
		size := binary.BigEndian.Uint32(hdr[4:])
		frame := make([]byte, size)
		if _, err := io.ReadFull(br, frame); err != nil {
			return nil
		}
		pending.Write(frame)
		var lines []string
		for {
			s := pending.String()
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				break
			}
			lines = append(lines, strings.TrimSuffix(s[:i], "\r"))
			pending.Reset()
			pending.WriteString(s[i+1:])
		}
		if len(lines) > 0 {
			fn(lines)
		}
	}
	return nil
}

// ComposeFollow streams merged project logs via the compose CLI (which owns
// the service-name prefixes) until ctx is canceled.
func ComposeFollow(ctx context.Context, project string, tail int, fn func(lines []string)) error {
	if tail <= 0 {
		tail = 200
	}
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", project,
		"logs", "--follow", "--tail", strconv.Itoa(tail))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fn([]string{sc.Text()})
	}
	return cmd.Wait()
}
