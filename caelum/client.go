package main

import (
	"os"
	"log"
	"path/filepath"
	"fmt"
	"net/url"
	"strings"
	"bytes"
	"io/ioutil"
	"mime"
	"net/http"
	"encoding/json"
	"code.google.com/p/go.net/websocket"
	//"code.google.com/p/go-uuid/uuid"
)

type HttpError interface {
	error
	StatusCode() int
}

// Errors
type HandlingError struct { 
	message string
}

func (self *HandlingError) StatusCode() int {
	return 500
}

func (self *HandlingError) Error() string {
	return self.message
}

func NewHandlingError(message string) HttpError {
	return &HandlingError { message }
}

type httpError struct {
	statusCode int
	message string
}

func (self *httpError) Error() string {
	return self.message
}

func (self *httpError) StatusCode() int {
	return self.statusCode
}

func NewHttpError(statusCode int, message string) HttpError {
	return &httpError { statusCode, message }
}

func safePath(rootPath string, path string) (string, error) {
	absPath, err := filepath.Abs(filepath.Join(rootPath, path))
	if err != nil {
		return "", NewHttpError(500, err.Error())
	}
	if !strings.HasPrefix(absPath, rootPath) {
		return "", NewHandlingError("Hacking attempt")
	}
	return absPath, nil
}

// TODO clean this up
var rootPath string

func handleRequest(requestChannel chan []byte, responseChannel chan []byte, closeChannel chan bool) {
	commandBuffer, ok := <-requestChannel
	if !ok {
		return
	}
	command := string(commandBuffer)
	// headers
	_, ok = <-requestChannel
	if !ok {
		return
	}

	var err HttpError
	commandParts := strings.Split(command, " ")
	switch commandParts[0] {
	case "GET":
		err = handleGet(commandParts[1], requestChannel, responseChannel)
	case "HEAD":
		err = handleHead(commandParts[1], requestChannel, responseChannel)
	case "PUT":
		err = handlePut(commandParts[1], requestChannel, responseChannel)
	case "DELETE":
		err = handleDelete(commandParts[1], requestChannel, responseChannel)
	case "POST":
		err = handlePost(commandParts[1], requestChannel, responseChannel)
	}
	if err != nil {
		sendError(responseChannel, err, commandParts[0] != "HEAD")
	}
	responseChannel <- DELIMITERBUFFER
	closeChannel <- true
}

func sendError(responseChannel chan []byte, err HttpError, withMessageInBody bool) {
	responseChannel <- statusCodeBuffer(err.StatusCode())

	if withMessageInBody {
		responseChannel <- headerBuffer(map[string]string { "Content-Type": "text/plain"})
		responseChannel <- []byte(err.Error())
	} else {
		responseChannel <- headerBuffer(map[string]string { "Content-Length": "0"})
	}
}

func dropUntilDelimiter(requestChannel chan []byte) {
	for {
		buffer, ok := <-requestChannel
		if !ok {
			break
		}
		if IsDelimiter(buffer) {
			break
		}
	}
}

func headerBuffer(headers map[string]string) []byte {
	var headerBuffer bytes.Buffer
	for h, v := range headers {
		headerBuffer.Write([]byte(fmt.Sprintf("%s: %s\n", h, v)))
	}
	bytes := headerBuffer.Bytes()
	return bytes[:len(bytes)-1]
}

func statusCodeBuffer(code int) []byte {
	return IntToBytes(code)
}

func handleGet(path string, requestChannel chan[]byte, responseChannel chan []byte) HttpError {
	fmt.Println("Requested:", path, rootPath)
	dropUntilDelimiter(requestChannel)
	safePath, err := safePath(rootPath, path)
	if err != nil {
		return err.(HttpError)
	}
	stat, err := os.Stat(safePath)
	if err != nil {
		return NewHttpError(404, "Not found")
	}
	responseChannel <- statusCodeBuffer(200)
	if stat.IsDir() {
		responseChannel <- headerBuffer(map[string]string {"Content-Type": "text/plain"})
		files, _ := ioutil.ReadDir(safePath)
		fmt.Println("Full path listing of", safePath)
		for _, f := range files {
			if f.Name()[0] == '.' {
				continue
			}
			fmt.Println("Sending:", f.Name())
			if f.IsDir() {
				responseChannel <- []byte(fmt.Sprintf("%s/\n", f.Name()))
			} else {
				responseChannel <- []byte(fmt.Sprintf("%s\n", f.Name()))
			}
		}
	} else { // File
		mimeType := mime.TypeByExtension(filepath.Ext(safePath))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		responseChannel <- headerBuffer(map[string]string {
			"Content-Type": mimeType,
			"ETag": stat.ModTime().String(),
		})
		f, err := os.Open(safePath)
		if err != nil {
			return NewHttpError(500, "Could not open file")
		}
		defer f.Close()
		for {
			buffer := make([]byte, BUFFER_SIZE)
			n, _ := f.Read(buffer)
			if n == 0 {
				break
			}
			responseChannel <- buffer[:n]
		}
	}
	return nil
}

func handleHead(path string, requestChannel chan[] byte, responseChannel chan []byte) HttpError {
	safePath, err := safePath(rootPath, path)
	dropUntilDelimiter(requestChannel)
	if err != nil {
		return err.(HttpError)
	}
	stat, err := os.Stat(safePath)
	if err != nil {
		return NewHttpError(404, "Not found")
	}
	responseChannel <- statusCodeBuffer(200)
	responseChannel <- headerBuffer(map[string]string {
		"ETag": stat.ModTime().String(),
		"Content-Length": "0",
	})
	return nil
}

func handlePut(path string, requestChannel chan[] byte, responseChannel chan []byte) HttpError {
	safePath, err := safePath(rootPath, path)
	if err != nil {
		dropUntilDelimiter(requestChannel)
		return err.(HttpError)
	}
	dir := filepath.Base(safePath)
	os.MkdirAll(dir, 0700)
	f, err := os.Create(safePath)
	if err != nil {
		dropUntilDelimiter(requestChannel)
		return NewHttpError(500, "Could not create file")
	}
	for {
		buffer := <-requestChannel
		if IsDelimiter(buffer) {
			break
		}
		_, err := f.Write(buffer)
		if err != nil {
			dropUntilDelimiter(requestChannel)
			return NewHttpError(500, "Could not write to file")
		}
	}
	f.Close()
	stat, _ := os.Stat(safePath)
	responseChannel <- statusCodeBuffer(200)
	responseChannel <- headerBuffer(map[string]string {
		"Content-Type": "text/plain",
		"ETag": stat.ModTime().String(),
	})
	responseChannel <- []byte("OK")
	return nil
}



func handleDelete(path string, requestChannel chan[] byte, responseChannel chan []byte) HttpError {
	safePath, err := safePath(rootPath, path)
	dropUntilDelimiter(requestChannel)
	if err != nil {
		return err.(HttpError)
	}
	_, err = os.Stat(safePath)
	if err != nil {
		return NewHttpError(404, "Not found")
	}
	err = os.Remove(safePath)
	if err != nil {
		return NewHttpError(500, "Could not delete")
	}
	responseChannel <- statusCodeBuffer(200)
	responseChannel <- headerBuffer(map[string]string {
		"Content-Type": "text/plain",
	})
	responseChannel <- []byte("OK")

	return nil
}

func walkDirectory(responseChannel chan []byte, root string, path string) {
	files, _ := ioutil.ReadDir(filepath.Join(root, path))
	for _, f := range files {
		if f.Name()[0] == '.' {
			continue
		}
		if f.IsDir() {
			walkDirectory(responseChannel, root, filepath.Join(path, f.Name()))
		} else {
			responseChannel <- []byte(fmt.Sprintf("/%s\n", filepath.Join(path, f.Name())))
		}
	}
}

func readWholeBody(requestChannel chan []byte) []byte {
	var byteBuffer bytes.Buffer
	for {
		buffer := <-requestChannel
		if IsDelimiter(buffer) {
			break
		}
		byteBuffer.Write(buffer)
	}
	return byteBuffer.Bytes()
}

func handlePost(path string, requestChannel chan[] byte, responseChannel chan []byte) HttpError {
	safePath, err := safePath(rootPath, path)
	body := string(readWholeBody(requestChannel))
	if err != nil {
		return err.(HttpError)
	}
	_, err = os.Stat(safePath)
	if err != nil {
		return NewHttpError(http.StatusNotFound, "Not found")
	}

	queryValues, err := url.ParseQuery(body)
	if err != nil {
		return NewHttpError(http.StatusInternalServerError, "Could not parse body as HTTP post")
	}

	action := queryValues["action"][0]
	switch action {
	case "filelist":
		responseChannel <- statusCodeBuffer(200)
		responseChannel <- headerBuffer(map[string]string {
			"Content-Type": "text/plain",
		})
		walkDirectory(responseChannel, safePath, "")
	default:
		return NewHttpError(http.StatusNotImplemented, "No such action")
	}

	return nil
}

func RunClient(hostname string, port int, path string) {
	origin := fmt.Sprintf("http://%s", hostname)
	url := fmt.Sprintf("ws://%s:%d/socket", hostname, port)
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		log.Fatal(err)
	}
	id := "random" // uuid.New()

	buffer, _ := json.Marshal(HelloMessage{"0.1", id})

	if _, err := ws.Write(buffer); err != nil {
		log.Fatal(err)
		return
	}
	rootPath = path
	fmt.Println("ID:", id)
	go PrintStats()
	multiplexer := NewRPCMultiplexer(ws, handleRequest)
	multiplexer.Multiplex()
}
