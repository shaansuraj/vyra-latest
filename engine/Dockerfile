FROM oven/bun:1.2.22-alpine
WORKDIR /app

COPY package.json bun.lock* ./
RUN bun install --frozen-lockfile || bun install

COPY . .

EXPOSE 5000
HEALTHCHECK --interval=15s --timeout=3s --retries=5 CMD wget -qO- http://127.0.0.1:5000/version > /dev/null || exit 1

CMD ["bun", "run", "index.ts"]
