package gps

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var DEBUG, _ = strconv.ParseBool(os.Getenv("TY_GIT_MIRROR_DEBUG"))
var MIRROR_NOT_FORCE, _ = strconv.ParseBool(os.Getenv("TY_GIT_MIRROR_NOT_FORCE"))
var MIRRORDOMAIN = os.Getenv("TY_GIT_MIRROR")

type mirrorReadCloser struct {
	s        []byte
	i        int64 // current reading index
	prevRune int   // index of previous rune; or < 0
	io.ReadCloser
}

func (r *mirrorReadCloser) Read(b []byte) (n int, err error) {
	if r.i >= int64(len(r.s)) {
		return 0, io.EOF
	}
	r.prevRune = -1
	n = copy(b, r.s[r.i:])
	r.i += int64(n)
	return
}

func (r *mirrorReadCloser) Close() error {
	return nil
}

func doFetchMetadataMirror(ctx context.Context, scheme, path string) (io.ReadCloser, error) {
	if DEBUG {
		fmt.Println("doFetchMetadataMirror in->", scheme, path)
	}
	switch scheme {
	case "https", "http":
		// 原始请求地址，这里统一转换为https协议
		urlOrg := fmt.Sprintf("https://%s?go-get=1", path)
		tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}

		// 只有设置了镜像且请求的路径为非当前镜像时才进行镜像处理
		if len(MIRRORDOMAIN) > 0 && strings.Index(path, MIRRORDOMAIN) < 0 {
			// 转换类似：golang.org/x/sys/unix 的情况至 golang.org/x/sys
			for i := strings.Count(path, "/") + 1; i > 1; i-- {
				// 我们的镜像始终使用https协议
				pathMirror := strings.Join(strings.Split(path, "/")[0:i], ".")
				urlMirror := fmt.Sprintf("https://%s/mirror/%s?go-get=1", MIRRORDOMAIN, pathMirror)
				fmt.Println("doFetchMetadataMirror Try->", urlMirror, urlOrg)

				req, err := http.NewRequest("GET", urlMirror, nil)
				if err == nil {
					// 忽略https的证书问题
					client := &http.Client{Transport: tr}
					resp, err := client.Do(req.WithContext(ctx))
					if err == nil {
						defer resp.Body.Close()
						body, _ := ioutil.ReadAll(resp.Body)
						bodyStr := string(body)
						if DEBUG {
							fmt.Println("doFetchMetadataMirror body->", urlMirror, bodyStr)
						}

						if strings.Index(bodyStr, "\"go-import\"") < 0 {
							// 镜像服务器返回了错误信息，使用原始地址进行重试
							if DEBUG {
								fmt.Println("go-import not found in Mirror URL response", urlMirror)
							}
						} else {
							// 镜像服务器返回OK
							fmt.Println("doFetchMetadataMirror OK->", urlMirror, urlOrg)
							rc := &mirrorReadCloser{s: body, i: 0, prevRune: -1}
							return rc, nil
						}
					} else {
						// 只是提醒
						if DEBUG {
							fmt.Println("failed HTTP request to Mirror URL", urlMirror, err.Error())
						}
					}
				} else {
					// 只是提醒
					if DEBUG {
						fmt.Println("unable to build HTTP request for Mirror URL", urlMirror, err.Error())
					}
				}
			}
		}

		if len(MIRRORDOMAIN) > 0 && strings.Index(path, MIRRORDOMAIN) < 0 {
			fmt.Println("doFetchMetadataMirror False use Original URL->", urlOrg)
		}

		// 修改原请求方式，忽略https的证书错误
		req, err := http.NewRequest("GET", urlOrg, nil)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to build HTTP request for URL %q", urlOrg)
		}

		client := &http.Client{Transport: tr}
		resp, err := client.Do(req.WithContext(ctx))
		if err != nil {
			return nil, errors.Wrapf(err, "failed HTTP request to URL %q", urlOrg)
		}
		return resp.Body, nil

	default:
		return nil, errors.Errorf("unknown remote protocol scheme: %q", scheme)
	}
}

func getMetadataMirror(ctx context.Context, path, scheme string) (string, string, string, error) {
	if DEBUG {
		fmt.Println("getMetadataMirror in->", scheme, path)
	}
	rc, err := fetchMetadata(ctx, path, scheme)
	if err != nil {
		if DEBUG {
			fmt.Println("getMetadataMirror fetch False->", path)
		}
		return "", "", "", errors.Wrapf(err, "unable to fetch raw metadata")
	}
	defer rc.Close()

	imports, err := parseMetaGoImports(rc)
	if err != nil {
		if DEBUG {
			fmt.Println("getMetadataMirror parse go-import False->", path)
		}
		return "", "", "", errors.Wrapf(err, "unable to parse go-import metadata")
	}
	match := -1
	for i, im := range imports {
		if !strings.HasPrefix(path, im.Prefix) {
			if len(MIRRORDOMAIN) > 0 && strings.Index(path, MIRRORDOMAIN) < 0 {
				// path = golang.org/x/crypto/acme/autocert
				// im.Prefix = tygit.touch4.me/mirror/golang.org.x.crypto
				// mirrorPath = golang.org.x.crypto.acme
				if DEBUG {
					fmt.Println("getMetadataMirror->", im.Prefix, path)
				}
				mirrorDomain := fmt.Sprintf("%s/mirror/", MIRRORDOMAIN)
				mirrorPrefix := strings.Replace(im.Prefix, mirrorDomain, "", -1)
				pathTks := strings.Split(path, "/")
				for i := strings.Count(path, "/") + 1; i > 1; i-- {
					pathMirror := strings.Join(pathTks[0:i], ".")
					if DEBUG {
						fmt.Println("getMetadataMirror->", mirrorPrefix, pathMirror)
					}
					if pathMirror == mirrorPrefix {
						// pathOrg = golang.org/x/crypto
						// repoRoot = https://golang.org/x/crypto.git
						pathOrg := strings.Join(pathTks[0:i], "/")
						repoRoot := fmt.Sprint("https://", pathOrg, ".git")
						if DEBUG {
							fmt.Println("getMetadataMirror OK->", pathOrg, "git", repoRoot, path)
						}
						return pathOrg, "git", repoRoot, nil
					}
				}
			}
			continue
		}
		if match != -1 {
			if DEBUG {
				fmt.Println("getMetadataMirror multiple False->", path)
			}
			return "", "", "", errors.Errorf("multiple meta tags match import path %q", path)
		}
		match = i
	}
	if match == -1 {
		if DEBUG {
			fmt.Println("getMetadataMirror not found False->", path)
		}
		return "", "", "", errors.Errorf("go-import metadata not found")
	}
	if DEBUG {
		fmt.Println("getMetadataMirror OK->", imports[match].Prefix, imports[match].VCS, imports[match].RepoRoot)
	}
	return imports[match].Prefix, imports[match].VCS, imports[match].RepoRoot, nil
}

func gitGetMirror(ctx context.Context, r *gitRepo) error {
	if DEBUG {
		fmt.Println("gitGetMirror in->", r.Remote(), r.LocalPath())
	}
	var out []byte
	var err error
	if len(MIRRORDOMAIN) > 0 && strings.Index(r.Remote(), MIRRORDOMAIN) < 0 {
		mirrorDomain := fmt.Sprintf("https://%s/mirror/", MIRRORDOMAIN)
		mirrorUrl := strings.Replace(r.Remote(), "/", ".", -1)
		mirrorUrl = strings.Replace(mirrorUrl, "https:..", mirrorDomain, 1)
		mirrorUrl = strings.Replace(mirrorUrl, "http:..", mirrorDomain, 1)
		fmt.Println("gitGetMirror mirrorUrl->", mirrorUrl)

		cmd := commandContext(
			ctx,
			"git",
			"clone",
			"--recursive",
			"-v",
			"--progress",
			mirrorUrl,
			r.LocalPath(),
		)
		// Ensure no prompting for PWs
		cmd.SetEnv(append([]string{"GIT_ASKPASS=", "GIT_TERMINAL_PROMPT=0"}, os.Environ()...))
		out, err = cmd.CombinedOutput()
		if err != nil {
			fmt.Println("WARRING1 please make mirror from", r.Remote(), "to", mirrorUrl)
			fmt.Println("WARRING1 If you want to continue with original URL, please set TY_GIT_MIRROR_NOT_FORCE=true")
			if !MIRROR_NOT_FORCE {
				return newVcsRemoteErrorOr(err, cmd.Args(), string(out),
					"unable to get repository")
			}
		} else {
			if DEBUG {
				fmt.Println("gitGetMirror OK->", mirrorUrl)
			}
			return nil
		}
	}

	fmt.Println("gitGetMirror original->", r.Remote())
	cmd := commandContext(
		ctx,
		"git",
		"clone",
		"--recursive",
		"-v",
		"--progress",
		r.Remote(),
		r.LocalPath(),
	)
	// Ensure no prompting for PWs
	cmd.SetEnv(append([]string{"GIT_ASKPASS=", "GIT_TERMINAL_PROMPT=0"}, os.Environ()...))
	if out, err = cmd.CombinedOutput(); err != nil {
		return newVcsRemoteErrorOr(err, cmd.Args(), string(out),
			"unable to get repository")
	}

	return nil
}

func listVersionsMirror(ctx context.Context, r ctxRepo) (out []byte, err error) {
	if DEBUG {
		fmt.Println("listVersionsMirror in->", r.Remote(), r.LocalPath())
	}
	if len(MIRRORDOMAIN) > 0 && strings.Index(r.Remote(), MIRRORDOMAIN) < 0 {
		mirrorDomain := fmt.Sprintf("https://%s/mirror/", MIRRORDOMAIN)
		mirrorUrl := strings.Replace(r.Remote(), "/", ".", -1)
		mirrorUrl = strings.Replace(mirrorUrl, "https:..", mirrorDomain, 1)
		mirrorUrl = strings.Replace(mirrorUrl, "http:..", mirrorDomain, 1)
		fmt.Println("listVersionsMirror ->", mirrorUrl)

		cmd := commandContext(ctx, "git", "ls-remote", mirrorUrl)
		// We want to invoke from a place where it's not possible for there to be a
		// .git file instead of a .git directory, as git ls-remote will choke on the
		// former and erroneously quit. However, we can't be sure that the repo
		// exists on disk yet at this point; if it doesn't, then instead use the
		// parent of the local path, as that's still likely a good bet.
		if r.CheckLocal() {
			cmd.SetDir(r.LocalPath())
		} else {
			cmd.SetDir(filepath.Dir(r.LocalPath()))
		}
		// Ensure no prompting for PWs
		cmd.SetEnv(append([]string{"GIT_ASKPASS=", "GIT_TERMINAL_PROMPT=0"}, os.Environ()...))
		out, err = cmd.CombinedOutput()
		if err != nil {
			fmt.Println("WARRING2 please make mirror from", r.Remote(), "to", mirrorUrl)
			fmt.Println("WARRING2 If you want to continue with original URL, please set TY_GIT_MIRROR_NOT_FORCE=true")
			if !MIRROR_NOT_FORCE {
				return nil, errors.Wrap(err, string(out))
			} else {
				return nil, nil
			}
		}
		return out, nil
	}
	return nil, nil
}
