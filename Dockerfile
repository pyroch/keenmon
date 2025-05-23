# Базовый образ на основе Python 3.13.3
FROM python:3.13.3-slim

# Установка рабочей директории внутри контейнера
WORKDIR /app

# Копирование файла зависимостей в контейнер
COPY requirements.txt .

# Установка зависимостей
RUN pip install --no-cache-dir -r requirements.txt

# Копирование исходного кода приложения в контейнер
COPY keenetic_exporter.py .

# Команда для запуска приложения
CMD ["python", "keenetic_exporter.py"]
