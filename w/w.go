package w

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	//"time"
	"bytes"
	"io"
	"mime/multipart"
	"os"
	//      "github.com/abbot/go-http-auth"
	//      "crypto/subtle"
)

func handler(w http.ResponseWriter, r *http.Request) {
	u := r.URL
	name := u.Query().Get("name")
	fmt.Fprintf(w, "Hi there, I love %s!", name)
}

func fexists(fpath string) bool {
	_, err := os.Stat(fpath)
	return err == nil
}

// upload logic
func upload(w http.ResponseWriter, r *http.Request) {

	fmt.Println("method:", r.Method)
	if r.Method == "GET" {
		formstr := `<html>
<head>
       <title>Upload file</title>
</head>
<body>
<form enctype="multipart/form-data" action="upload" method="post">
    <input type="file" name="uploadfile" />
    <input type="hidden" name="token" value="{{.}}"/>
    <input type="submit" value="upload" />
</form>
</body>
</html>`
		fmt.Fprint(w, formstr)
		/*crutime := time.Now().Unix()
		  h := md5.New()
		  io.WriteString(h, strconv.FormatInt(crutime, 10))
		  token := fmt.Sprintf("%x", h.Sum(nil))

		  t, _ := template.ParseFiles("upload.gtpl")
		  t.Execute(w, token) */
	} else {
		r.ParseMultipartForm(32 << 20)
		file, handler, err := r.FormFile("uploadfile")
		if err != nil {
			fmt.Println(err)
			return
		}
		defer file.Close()
		tpath := r.FormValue("tpath")
		if tpath == "" {
			tpath = "./test"
		}
		fmt.Fprintf(w, "%v", handler.Header)
		if !fexists(tpath) {
			os.MkdirAll(tpath, 0755)
		}
		f, err := os.OpenFile(tpath+"/"+handler.Filename, os.O_WRONLY|os.O_CREATE, 0666)
		if err != nil {
			fmt.Println(err)
			return
		}
		defer f.Close()
		io.Copy(f, file)
	}
}

func Upload_client(args []string) {
	if len(args) < 2 {
		log.Printf("args: <url> <filename>\n")
		return
	}
	file := args[0]
	url := args[1]
	// Prepare a form that you will submit to that URL.
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	// Add your image file
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()
	fw, err := w.CreateFormFile("uploadfile", file)
	if err != nil {
		return
	}
	if _, err = io.Copy(fw, f); err != nil {
		return
	}
	// Add the other fields
	if fw, err = w.CreateFormField("tpath"); err != nil {
		return
	}
	if _, err = fw.Write([]byte("")); err != nil {
		return
	}
	// Don't forget to close the multipart writer.
	// If you don't close it, your request will be missing the terminating boundary.
	w.Close()

	// Now that you have a form, you can submit it to your handler.
	req, err := http.NewRequest("POST", url, &b)
	if err != nil {
		return
	}
	// Don't forget to set the content type, this will contain the boundary.
	req.Header.Set("Content-Type", w.FormDataContentType())

	// Submit the request
	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		return
	}

	// Check the response
	if res.StatusCode != http.StatusOK {
		err = fmt.Errorf("bad status: %s", res.Status)
	}
	return
}

// 如何使用basic auth, https://stackoverflow.com/questions/25552107/golang-how-to-serve-static-files-with-basic-authentication

func doRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "<h1>static file server</h1><p><a href='./static'>folder</p>")
}
func handleFileServer(dir, prefix string) http.HandlerFunc {
	fs := http.FileServer(http.Dir(dir))
	realHandler := http.StripPrefix(prefix, fs).ServeHTTP
	return func(w http.ResponseWriter, req *http.Request) {
		// log.Println(req.URL)
		realHandler(w, req)
	}
}

func doHello(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hello world!\n")
}

func Run(args []string) {
	cmd := flag.NewFlagSet("w", flag.ExitOnError)
	svrport := cmd.Int("p", 9191, "server port")
	svrdir := cmd.String("d", "static", "dir for static file")
	prefix := cmd.String("x", "", "URI prefix")
	cmd.Parse(args)

	http.HandleFunc(fmt.Sprintf("%s/", *prefix), doRoot)
	http.HandleFunc(fmt.Sprintf("%s/hello", *prefix), doHello)

	http.HandleFunc(fmt.Sprintf("%s/test11", *prefix), handler)
	http.HandleFunc(fmt.Sprintf("%s/upload", *prefix), upload)
	uri := fmt.Sprintf("%s/%s/", *prefix, *svrdir)
	mydir := fmt.Sprintf("%s/", *svrdir)
	http.Handle(uri, http.StripPrefix(uri, http.FileServer(http.Dir(mydir))))

	log.Printf("Serve on %d for dir %s,  prefix: [%s]\n", *svrport, *svrdir, *prefix)
	// log.Printf("test env: _PORT=[%s]\n", os.Getenv("_PORT"))
	//fmt.Printf("serve on %d for dir[%s]\n", *svrport, *svrdir)
	http.ListenAndServe(fmt.Sprintf(":%d", *svrport), nil)
}

// ./w -port 9191 -dir kf
