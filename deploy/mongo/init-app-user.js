// Runs once, when the MongoDB volume is created (docker-entrypoint-initdb.d).
// The API connects with this least-privilege user: readWrite on its own database only.
const dbName = process.env.MONGO_DATABASE;
const user = process.env.MONGO_APP_USERNAME;
const pwd = process.env.MONGO_APP_PASSWORD;

if (!dbName || !user || !pwd) {
  throw new Error("MONGO_DATABASE, MONGO_APP_USERNAME and MONGO_APP_PASSWORD are required");
}

db.getSiblingDB(dbName).createUser({
  user,
  pwd,
  roles: [{ role: "readWrite", db: dbName }],
});
