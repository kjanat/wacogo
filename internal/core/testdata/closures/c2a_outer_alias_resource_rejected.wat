(component $Root
  (type $R (resource (rep i32)))
  (component $N
    (alias outer $Root $R (type $R2))
    (core func $drop2 (canon resource.drop $R2))))
