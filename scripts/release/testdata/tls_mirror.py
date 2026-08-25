"""Serve a directory over HTTPS for get.sh installer tests.

get.sh pins `--proto '=https'` on every fetch so a redirect cannot downgrade
an install to plaintext. That guard is correct and must not be relaxed to
make testing easier, so the test mirror speaks TLS with a throwaway
self-signed certificate.

Usage: tls_mirror.py <directory> <port> <cert.pem>
"""

import functools
import http.server
import ssl
import sys


def main() -> None:
    directory, port, cert = sys.argv[1], int(sys.argv[2]), sys.argv[3]
    handler = functools.partial(
        http.server.SimpleHTTPRequestHandler, directory=directory
    )
    httpd = http.server.HTTPServer(("127.0.0.1", port), handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert)
    httpd.socket = context.wrap_socket(httpd.socket, server_side=True)
    httpd.serve_forever()


if __name__ == "__main__":
    main()
