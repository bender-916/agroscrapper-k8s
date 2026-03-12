CREATE TABLE IF NOT EXISTS cursos (
    id SERIAL PRIMARY KEY,
    url VARCHAR(2048) UNIQUE NOT NULL,
    titulo VARCHAR(500),
    lugar VARCHAR(255),
    periodo VARCHAR(255),
    hora VARCHAR(100),
    plazas VARCHAR(50),
    costo VARCHAR(100),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_cursos_url ON cursos(url);
