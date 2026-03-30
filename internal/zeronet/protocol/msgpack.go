package protocol

import (
	"bufio"
	"fmt"
	"io"

	"github.com/vmihailenco/msgpack/v5"
)

// ReadMessage 从 reader 中按 msgpack 解出一条消息。
func ReadMessage(reader *bufio.Reader) (Message, error) {
	decoder := msgpack.NewDecoder(reader)
	decoder.SetCustomStructTag("msgpack")

	var msg Message
	if err := decoder.Decode(&msg); err != nil {
		if err == io.EOF {
			return nil, err
		}
		return nil, fmt.Errorf("解码 ZeroNet 消息失败: %w", err)
	}
	return msg, nil
}

// WriteMessage 将消息编码为 msgpack 后写入 writer。
func WriteMessage(writer io.Writer, msg Message) error {
	encoder := msgpack.NewEncoder(writer)
	encoder.SetCustomStructTag("msgpack")
	if err := encoder.Encode(msg); err != nil {
		return fmt.Errorf("编码 ZeroNet 消息失败: %w", err)
	}
	return nil
}
