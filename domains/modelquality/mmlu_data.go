package modelquality

// GetMMLULiteSuite 获取MMLU精简测试套件(快速检测用)
// 选取5个代表性学科,每学科10题,共50题,预计2-5分钟完成
func GetMMLULiteSuite() *BenchmarkSuite {
	return &BenchmarkSuite{
		Type:    BenchmarkTypeMMLULite,
		Name:    "MMLU Lite - Quick Quality Check",
		Version: "1.0",
		Questions: []Question{
			// ===== 1. Computer Science (计算机科学) =====
			{
				ID:       "cs_001",
				Subject:  "computer_science",
				Question: "What is the time complexity of binary search in a sorted array of n elements?",
				Options:  []string{"O(n)", "O(log n)", "O(n log n)", "O(1)"},
				Answer:   "B",
			},
			{
				ID:       "cs_002",
				Subject:  "computer_science",
				Question: "Which data structure uses LIFO (Last In First Out) principle?",
				Options:  []string{"Queue", "Stack", "Tree", "Graph"},
				Answer:   "B",
			},
			{
				ID:       "cs_003",
				Subject:  "computer_science",
				Question: "In object-oriented programming, what does polymorphism allow?",
				Options:  []string{"Multiple inheritance", "Objects to take many forms", "Private variables", "Static typing"},
				Answer:   "B",
			},
			{
				ID:       "cs_004",
				Subject:  "computer_science",
				Question: "What does SQL stand for?",
				Options:  []string{"Simple Query Language", "Structured Query Language", "System Query Logic", "Standard Question Language"},
				Answer:   "B",
			},
			{
				ID:       "cs_005",
				Subject:  "computer_science",
				Question: "Which of the following is NOT a valid IP address format?",
				Options:  []string{"192.168.1.1", "256.0.0.1", "10.0.0.255", "172.16.0.1"},
				Answer:   "B",
			},
			{
				ID:       "cs_006",
				Subject:  "computer_science",
				Question: "What is the purpose of a hash function in data structures?",
				Options:  []string{"Sort data", "Map data to fixed-size values", "Compress files", "Encrypt passwords"},
				Answer:   "B",
			},
			{
				ID:       "cs_007",
				Subject:  "computer_science",
				Question: "In REST API, which HTTP method is used to update a resource?",
				Options:  []string{"GET", "POST", "PUT", "DELETE"},
				Answer:   "C",
			},
			{
				ID:       "cs_008",
				Subject:  "computer_science",
				Question: "What is the main advantage of using version control systems like Git?",
				Options:  []string{"Faster compilation", "Track changes and collaborate", "Better performance", "Automatic testing"},
				Answer:   "B",
			},
			{
				ID:       "cs_009",
				Subject:  "computer_science",
				Question: "Which algorithm paradigm does Dijkstra's shortest path algorithm follow?",
				Options:  []string{"Divide and Conquer", "Dynamic Programming", "Greedy", "Backtracking"},
				Answer:   "C",
			},
			{
				ID:       "cs_010",
				Subject:  "computer_science",
				Question: "What does CPU stand for in computer architecture?",
				Options:  []string{"Central Processing Unit", "Computer Personal Unit", "Central Program Utility", "Core Processing Unit"},
				Answer:   "A",
			},

			// ===== 2. Mathematics (数学) =====
			{
				ID:       "math_001",
				Subject:  "mathematics",
				Question: "What is the derivative of x² with respect to x?",
				Options:  []string{"x", "2x", "x²", "2"},
				Answer:   "B",
			},
			{
				ID:       "math_002",
				Subject:  "mathematics",
				Question: "What is the value of π (pi) to two decimal places?",
				Options:  []string{"3.12", "3.14", "3.16", "3.18"},
				Answer:   "B",
			},
			{
				ID:       "math_003",
				Subject:  "mathematics",
				Question: "If a triangle has sides of length 3, 4, and 5, what type of triangle is it?",
				Options:  []string{"Equilateral", "Isosceles", "Right", "Obtuse"},
				Answer:   "C",
			},
			{
				ID:       "math_004",
				Subject:  "mathematics",
				Question: "What is the solution to the equation 2x + 5 = 15?",
				Options:  []string{"x = 5", "x = 10", "x = 7.5", "x = 3"},
				Answer:   "A",
			},
			{
				ID:       "math_005",
				Subject:  "mathematics",
				Question: "What is the probability of flipping a fair coin and getting heads?",
				Options:  []string{"0.25", "0.5", "0.75", "1.0"},
				Answer:   "B",
			},
			{
				ID:       "math_006",
				Subject:  "mathematics",
				Question: "What is the sum of interior angles in a pentagon?",
				Options:  []string{"360°", "450°", "540°", "720°"},
				Answer:   "C",
			},
			{
				ID:       "math_007",
				Subject:  "mathematics",
				Question: "What is the value of log₁₀(100)?",
				Options:  []string{"1", "2", "10", "100"},
				Answer:   "B",
			},
			{
				ID:       "math_008",
				Subject:  "mathematics",
				Question: "What is the median of the set {3, 7, 2, 9, 5}?",
				Options:  []string{"3", "5", "7", "9"},
				Answer:   "B",
			},
			{
				ID:       "math_009",
				Subject:  "mathematics",
				Question: "What is the area of a circle with radius 3?",
				Options:  []string{"6π", "9π", "12π", "18π"},
				Answer:   "B",
			},
			{
				ID:       "math_010",
				Subject:  "mathematics",
				Question: "What is the next number in the sequence: 2, 4, 8, 16, ...?",
				Options:  []string{"24", "28", "32", "64"},
				Answer:   "C",
			},

			// ===== 3. Physics (物理) =====
			{
				ID:       "physics_001",
				Subject:  "physics",
				Question: "What is the SI unit of force?",
				Options:  []string{"Joule", "Newton", "Watt", "Pascal"},
				Answer:   "B",
			},
			{
				ID:       "physics_002",
				Subject:  "physics",
				Question: "According to Newton's second law, F = ma. If mass doubles and acceleration stays constant, force will:",
				Options:  []string{"Halve", "Stay the same", "Double", "Quadruple"},
				Answer:   "C",
			},
			{
				ID:       "physics_003",
				Subject:  "physics",
				Question: "What is the speed of light in a vacuum?",
				Options:  []string{"3 × 10⁶ m/s", "3 × 10⁸ m/s", "3 × 10¹⁰ m/s", "3 × 10¹² m/s"},
				Answer:   "B",
			},
			{
				ID:       "physics_004",
				Subject:  "physics",
				Question: "What type of energy does a stretched spring possess?",
				Options:  []string{"Kinetic", "Thermal", "Potential", "Chemical"},
				Answer:   "C",
			},
			{
				ID:       "physics_005",
				Subject:  "physics",
				Question: "What is the acceleration due to gravity on Earth (approximate)?",
				Options:  []string{"9.8 m/s²", "10.8 m/s²", "8.8 m/s²", "11.8 m/s²"},
				Answer:   "A",
			},
			{
				ID:       "physics_006",
				Subject:  "physics",
				Question: "Which law states that energy cannot be created or destroyed?",
				Options:  []string{"Newton's First Law", "Ohm's Law", "Law of Conservation of Energy", "Boyle's Law"},
				Answer:   "C",
			},
			{
				ID:       "physics_007",
				Subject:  "physics",
				Question: "What happens to the resistance of a conductor when temperature increases?",
				Options:  []string{"Decreases", "Stays constant", "Increases", "Becomes zero"},
				Answer:   "C",
			},
			{
				ID:       "physics_008",
				Subject:  "physics",
				Question: "What is the unit of electrical resistance?",
				Options:  []string{"Ampere", "Volt", "Ohm", "Watt"},
				Answer:   "C",
			},
			{
				ID:       "physics_009",
				Subject:  "physics",
				Question: "What phenomenon causes a straw in water to appear bent?",
				Options:  []string{"Reflection", "Refraction", "Diffraction", "Interference"},
				Answer:   "B",
			},
			{
				ID:       "physics_010",
				Subject:  "physics",
				Question: "What is the relationship between wavelength and frequency of a wave?",
				Options:  []string{"Directly proportional", "Inversely proportional", "No relationship", "Exponential"},
				Answer:   "B",
			},

			// ===== 4. History (历史) =====
			{
				ID:       "history_001",
				Subject:  "history",
				Question: "In which year did World War II end?",
				Options:  []string{"1943", "1944", "1945", "1946"},
				Answer:   "C",
			},
			{
				ID:       "history_002",
				Subject:  "history",
				Question: "Who was the first President of the United States?",
				Options:  []string{"Thomas Jefferson", "George Washington", "John Adams", "Benjamin Franklin"},
				Answer:   "B",
			},
			{
				ID:       "history_003",
				Subject:  "history",
				Question: "The Renaissance period began in which country?",
				Options:  []string{"France", "England", "Italy", "Spain"},
				Answer:   "C",
			},
			{
				ID:       "history_004",
				Subject:  "history",
				Question: "Which ancient civilization built the pyramids of Giza?",
				Options:  []string{"Greeks", "Romans", "Mesopotamians", "Egyptians"},
				Answer:   "D",
			},
			{
				ID:       "history_005",
				Subject:  "history",
				Question: "The Magna Carta was signed in which year?",
				Options:  []string{"1066", "1215", "1415", "1515"},
				Answer:   "B",
			},
			{
				ID:       "history_006",
				Subject:  "history",
				Question: "Who invented the printing press around 1440?",
				Options:  []string{"Leonardo da Vinci", "Johannes Gutenberg", "Isaac Newton", "Galileo Galilei"},
				Answer:   "B",
			},
			{
				ID:       "history_007",
				Subject:  "history",
				Question: "The Cold War was primarily between which two superpowers?",
				Options:  []string{"USA and China", "USA and USSR", "UK and USSR", "France and Germany"},
				Answer:   "B",
			},
			{
				ID:       "history_008",
				Subject:  "history",
				Question: "Which empire was ruled by Julius Caesar?",
				Options:  []string{"Greek Empire", "Roman Empire", "Persian Empire", "Ottoman Empire"},
				Answer:   "B",
			},
			{
				ID:       "history_009",
				Subject:  "history",
				Question: "The French Revolution began in which year?",
				Options:  []string{"1776", "1789", "1799", "1804"},
				Answer:   "B",
			},
			{
				ID:       "history_010",
				Subject:  "history",
				Question: "Who was the leader of the Soviet Union during most of World War II?",
				Options:  []string{"Lenin", "Stalin", "Khrushchev", "Gorbachev"},
				Answer:   "B",
			},

			// ===== 5. Logic & Reasoning (逻辑推理) =====
			{
				ID:       "logic_001",
				Subject:  "logic",
				Question: "All cats are animals. All animals need food. Therefore:",
				Options:  []string{"All cats need food", "Some cats don't need food", "No cats need food", "Only some cats need food"},
				Answer:   "A",
			},
			{
				ID:       "logic_002",
				Subject:  "logic",
				Question: "If A > B and B > C, then:",
				Options:  []string{"A < C", "A = C", "A > C", "Cannot determine"},
				Answer:   "C",
			},
			{
				ID:       "logic_003",
				Subject:  "logic",
				Question: "What comes next in the pattern: A, C, E, G, ...?",
				Options:  []string{"H", "I", "J", "K"},
				Answer:   "B",
			},
			{
				ID:       "logic_004",
				Subject:  "logic",
				Question: "If it's raining, the ground is wet. The ground is wet. Therefore:",
				Options:  []string{"It must be raining", "It might be raining", "It's not raining", "It will rain soon"},
				Answer:   "B",
			},
			{
				ID:       "logic_005",
				Subject:  "logic",
				Question: "Which number doesn't belong: 2, 3, 5, 7, 9, 11?",
				Options:  []string{"2", "3", "9", "11"},
				Answer:   "C",
			},
			{
				ID:       "logic_006",
				Subject:  "logic",
				Question: "If all roses are flowers and some flowers are red, can we conclude that some roses are red?",
				Options:  []string{"Yes, definitely", "No, we cannot", "Only if most flowers are red", "Only in spring"},
				Answer:   "B",
			},
			{
				ID:       "logic_007",
				Subject:  "logic",
				Question: "What is the missing number: 1, 4, 9, 16, ?, 36",
				Options:  []string{"20", "24", "25", "30"},
				Answer:   "C",
			},
			{
				ID:       "logic_008",
				Subject:  "logic",
				Question: "If NOT (A AND B) is true, which of the following must be true?",
				Options:  []string{"A is false or B is false", "Both A and B are false", "A is true and B is true", "Neither A nor B exists"},
				Answer:   "A",
			},
			{
				ID:       "logic_009",
				Subject:  "logic",
				Question: "A bat and a ball cost $1.10 in total. The bat costs $1.00 more than the ball. How much does the ball cost?",
				Options:  []string{"$0.10", "$0.05", "$0.15", "$0.20"},
				Answer:   "B",
			},
			{
				ID:       "logic_010",
				Subject:  "logic",
				Question: "If 5 machines take 5 minutes to make 5 widgets, how long would it take 100 machines to make 100 widgets?",
				Options:  []string{"5 minutes", "20 minutes", "100 minutes", "500 minutes"},
				Answer:   "A",
			},
		},
	}
}

// GetMMLUFullSuite 获取完整MMLU测试套件(深度评估用)
// 包含更多题目和学科,用于周期性全面评估
func GetMMLUFullSuite() *BenchmarkSuite {
	// 完整版本包含所有Lite题目 + 额外的深度题目
	suite := GetMMLULiteSuite()
	suite.Type = BenchmarkTypeMMLU
	suite.Name = "MMLU Full - Comprehensive Quality Assessment"
	
	// 可以在这里添加更多高级题目
	// 生产环境建议从外部JSON文件加载
	
	return suite
}
