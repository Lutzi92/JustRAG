import { defineConfig } from 'drizzle-kit';
import dotenv from 'dotenv';

dotenv.config();

export default defineConfig({
    schema: './src/db/schema_vector.ts',
    out: './drizzle/vector',
    dialect: 'postgresql',
    dbCredentials: {
        host: process.env.VECTOR_DB_HOST || process.env.DB_HOST || 'localhost',
        port: parseInt(process.env.VECTOR_DB_PORT || process.env.DB_PORT || '5432'),
        user: process.env.VECTOR_DB_USER || process.env.DB_USER || 'postgres',
        password: process.env.VECTOR_DB_PASSWORD || process.env.DB_PASSWORD || 'postgres',
        database: process.env.VECTOR_DB_NAME || process.env.DB_NAME || 'postgres',
        ssl: false,
    },
});
