run "greeting" {
  command = plan

  variables {
    name = "x"
  }

  assert {
    condition     = output.greeting == "hello x"
    error_message = "вывод greeting не совпал"
  }
}
