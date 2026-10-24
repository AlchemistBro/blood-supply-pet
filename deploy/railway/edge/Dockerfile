FROM nginx:1.27-alpine
COPY edge/default.conf.template /etc/nginx/templates/default.conf.template
COPY edge/15-private-dns.sh /docker-entrypoint.d/15-private-dns.envsh
RUN chmod 755 /docker-entrypoint.d/15-private-dns.envsh
EXPOSE 8080
