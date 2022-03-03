package main

import (
	"bytes"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

func humanizeBytes(b int64) string {
	const unit int64 = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := unit, 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "kMGTPE"[exp])
}

type responseWriter struct {
	http.ResponseWriter
	StatusCode int
}

func (w *responseWriter) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
	w.StatusCode = code
}

func withLogging(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &responseWriter{
			ResponseWriter: w,
			StatusCode:     http.StatusOK,
		}
		h.ServeHTTP(ww, r)
		log.Printf("[%s] [%v] [%d] %s %s %s", r.Method, time.Since(start), ww.StatusCode, r.Host, r.URL.Path, r.URL.RawQuery)
	}
}

type s3Client struct {
	s3iface.S3API
	bucket string
}

func (c *s3Client) listObjectsByPrefix(path string) ([]file, error) {
	var (
		continuationToken *string
		files             []file
		key               = strings.TrimPrefix(path, "/")
	)
	for {
		listObjectsV2Output, err := c.S3API.ListObjectsV2(&s3.ListObjectsV2Input{
			Bucket:            aws.String(c.bucket),
			Delimiter:         aws.String("/"),
			Prefix:            aws.String(key),
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, err
		}
		for _, object := range listObjectsV2Output.Contents {
			files = append(files, file{
				name:         aws.StringValue(object.Key),
				size:         aws.Int64Value(object.Size),
				lastModified: aws.TimeValue(object.LastModified),
			})
		}
		for _, prefix := range listObjectsV2Output.CommonPrefixes {
			files = append(files, file{
				name:  aws.StringValue(prefix.Prefix),
				isDir: true,
			})
		}
		if !aws.BoolValue(listObjectsV2Output.IsTruncated) || listObjectsV2Output.NextContinuationToken == nil {
			break
		}
		continuationToken = listObjectsV2Output.NextContinuationToken
	}
	return files, nil
}

func (c *s3Client) getPreSignedLink(key string, duration time.Duration) (string, error) {
	req, _ := c.GetObjectRequest(&s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	return req.Presign(duration)
}

type file struct {
	name         string
	isDir        bool
	size         int64
	lastModified time.Time
}

func (c *s3Client) renderFileList(w http.ResponseWriter, r *http.Request, list []file) (err error) {
	sort.Slice(list, func(i, j int) bool {
		return list[i].name < list[j].name
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<pre>\n")
	_, _ = fmt.Fprintf(w, "%s\n\n", r.URL.Path)
	if r.URL.Path != "/" {
		path := r.URL.Path[:strings.LastIndex(r.URL.Path, "/")]
		_, _ = fmt.Fprintf(w, "<a href=\"%s/..\">..</a>\n", path)
	}
	buf := bytes.NewBuffer(nil)
	tab := tabwriter.NewWriter(buf, 0, 0, 1, ' ', 0)
	for _, dir := range list {
		if dir.isDir {
			_, _ = fmt.Fprintf(tab, "%s\t\t\t\n", dir.name)
		} else {
			_, _ = fmt.Fprintf(tab, "%s\t%s\t%s\n", dir.name, dir.lastModified.Format(time.RFC3339), humanizeBytes(dir.size))
		}
	}
	_ = tab.Flush()
	content := buf.String()
	for _, dir := range list {
		link := ""
		if dir.isDir {
			link = fmt.Sprintf("/%s", dir.name)
		} else {
			link, err = c.getPreSignedLink(dir.name, time.Minute*5)
			if err != nil {
				return fmt.Errorf("error getting presigned link for %s: %v", dir.name, err)
			}
		}
		content = strings.Replace(content, dir.name, fmt.Sprintf("<a href=\"%s\">%s</a>", link, dir.name), 1)
	}
	_, _ = fmt.Fprint(w, content, "\n</pre>\n")
	return nil
}

func buildHandler(client *s3Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/") {
			list, err := client.listObjectsByPrefix(path)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err = client.renderFileList(w, r, list); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		} else {
			link, err := client.getPreSignedLink(path, time.Minute*5)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, link, http.StatusPermanentRedirect)
		}
	}
}

func main() {
	bucketName := os.Getenv("S3_BUCKET_NAME")
	sess, err := session.NewSession(&aws.Config{
		Region:      aws.String("us-east-1"),
		Endpoint:    aws.String(os.Getenv("S3_ENDPOINT")),
		Credentials: credentials.NewStaticCredentials(os.Getenv("S3_ACCESS_KEY_ID"), os.Getenv("S3_SECRET_ACCESS_KEY"), ""),
	})
	if err != nil {
		log.Fatal("failed to create AWS session: ", err)
	}
	client := &s3Client{
		S3API:  s3.New(sess),
		bucket: bucketName,
	}
	http.Handle("/", withLogging(buildHandler(client)))
	log.Fatal("failed to serve request: ", http.ListenAndServe(":8080", nil))
}
