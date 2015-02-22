imagemagickd
===

Start server on 127.0.0.1:8088

```
$ go run imagemagick_server.go
```

and open to

```
http://127.0.0.1:8888/fill/300/300/any_host/any_path
```

# Arguments

**"/#{method}/#{width}/#{height}/#{resource}"**

## methods

- fill
- fit
- and pluggable function by opts.yml

# feature

- Keep size file caching
- Parallel run
- Any host image read
- Use imagemagick
