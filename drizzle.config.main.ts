import { defineConfig } from 'drizzle-kit';
import dotenv from 'dotenv';

dotenv.config();

export default defineConfig({
    schema: ['./src/db/schema_main.ts', './src/db/schema_jobs.ts'],
    out: './drizzle/main',
    dialect: 'postgresql',
    dbCredentials: {
        host: process.env.DB_HOST || 'localhost',
        port: parseInt(process.env.DB_PORT || '5432'),
        user: process.env.DB_USER || 'postgres',
        password: process.env.DB_PASSWORD || 'postgres',
        database: process.env.DB_NAME || 'postgres',
        ssl: false,
    },
});
