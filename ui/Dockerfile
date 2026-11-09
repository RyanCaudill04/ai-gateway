# --- Build stage: compile the React app to static files ---
FROM node:24-alpine AS build
WORKDIR /app

# Install deps first so this layer is cached until package*.json changes
COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build

# --- Runtime stage: serve the static files ---
FROM nginx:1-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
