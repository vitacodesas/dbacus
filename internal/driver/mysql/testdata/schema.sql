DROP DATABASE IF EXISTS shop;
CREATE DATABASE shop;
USE shop;
CREATE TABLE customers (
  id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  email VARCHAR(190) NOT NULL,
  name VARCHAR(100) NULL DEFAULT NULL COMMENT 'Nombre completo',
  nickname VARCHAR(40) NOT NULL DEFAULT 'anon',
  created DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_email (email)
) COMMENT='Clientes';
CREATE TABLE orders (
  id INT AUTO_INCREMENT,
  customer_id INT UNSIGNED NOT NULL,
  status ENUM('new','paid') NOT NULL DEFAULT 'new',
  total DECIMAL(10,2) NOT NULL,
  PRIMARY KEY (id),
  KEY idx_status_total (status, total),
  CONSTRAINT fk_orders_customer FOREIGN KEY (customer_id) REFERENCES customers (id) ON DELETE CASCADE ON UPDATE RESTRICT
);
CREATE TABLE order_items (
  order_id INT NOT NULL,
  line INT NOT NULL,
  sku VARCHAR(40) NOT NULL,
  PRIMARY KEY (order_id, line),
  CONSTRAINT fk_items_order FOREIGN KEY (order_id) REFERENCES orders (id)
);
CREATE TABLE `weird``name` (`col one` INT, `select` INT);
CREATE VIEW paid_orders AS SELECT id, total FROM orders WHERE status = 'paid';
