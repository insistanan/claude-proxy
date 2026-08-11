package modelaudit

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

const maxSSELineBytes = 2 * 1024 * 1024

type sseMessage struct {
	Event string
	Data  string
}

func consumeSSE(reader io.Reader, consume func(sseMessage) error) error {
	if reader == nil {
		return fmt.Errorf("SSE 响应体不能为空")
	}
	if consume == nil {
		return fmt.Errorf("SSE 消费函数不能为空")
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxSSELineBytes)
	event := ""
	dataLines := make([]string, 0, 1)
	dispatch := func() error {
		if event == "" && len(dataLines) == 0 {
			return nil
		}
		message := sseMessage{Event: event, Data: strings.Join(dataLines, "\n")}
		event = ""
		dataLines = dataLines[:0]
		return consume(message)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found && strings.HasPrefix(value, " ") {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			event = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}
