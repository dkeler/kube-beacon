package commands

import (
	"encoding/json"
	"fmt"
	"github.com/Knetic/govaluate"
	"github.com/chen-keinan/beacon/internal/common"
	"github.com/chen-keinan/beacon/internal/logger"
	"github.com/chen-keinan/beacon/internal/models"
	"github.com/chen-keinan/beacon/internal/reports"
	"github.com/chen-keinan/beacon/internal/shell"
	"github.com/chen-keinan/beacon/pkg/utils"
	"github.com/kyokomi/emoji"
	"strconv"
	"strings"
)

var log = logger.GetLog()

//ValidateExprData expr data
type ValidateExprData struct {
	index     int
	resultArr []string
	atb       *models.AuditBench
	origSize  int
	Total     int
	Match     int
}

//NextValidExprData return the next recursive ValidExprData
func (ve ValidateExprData) NextValidExprData() ValidateExprData {
	return ValidateExprData{resultArr: ve.resultArr[1:ve.index], index: ve.index - 1, atb: ve.atb, origSize: ve.origSize}
}

// NewValidExprData return new instance of ValidExprData
func NewValidExprData(arr []string, at *models.AuditBench) ValidateExprData {
	return ValidateExprData{resultArr: arr, index: len(arr), atb: at, origSize: len(arr)}
}

//K8sAudit k8s benchmark object
type K8sAudit struct {
	Command     shell.Executor
	FailedTests []*models.AuditBench
	args        []string
}

//NewK8sAudit new audit object
func NewK8sAudit() *K8sAudit {
	return &K8sAudit{FailedTests: make([]*models.AuditBench, 0), Command: shell.NewShellExec()}
}

//Help return benchmark command help
func (bk K8sAudit) Help() string {
	return "-a , --audit run benchmark audit tests"
}

//Run execute benchmark command
func (bk *K8sAudit) Run(args []string) int {
	bk.args = args
	audit := models.Audit{}
	auditFiles, err := utils.GetK8sBenchAuditFiles()
	if err != nil {
		panic(fmt.Sprintf("failed to read audit files %s", err))
	}
	for _, auditFile := range auditFiles {
		err := json.Unmarshal([]byte(auditFile.Data), &audit)
		if err != nil {
			panic("Failed to unmarshal audit test json file")
		}
		for _, ac := range audit.Categories {
			bk.runTests(ac)
		}
	}
	reports.GenerateAuditReport(bk.FailedTests)

	return 0
}

func (bk *K8sAudit) runTests(ac models.Category) {
	for _, at := range ac.SubCategory.AuditTests {
		resArr := make([]string, 0)
		for index, val := range at.AuditCommand {
			cmd := bk.UpdateCommandParams(at, index, val, resArr)
			if cmd == "" {
				emptyValue := bk.addDummyCommandResponse(at, index)
				resArr = append(resArr, emptyValue)
				continue
			}
			result, _ := bk.Command.Exec(cmd)
			if result.Stderr != "" {
				resArr = append(resArr, "")
				log.Console(fmt.Sprintf("Failed to execute command %s", result.Stderr))
				continue
			}
			resArr = append(resArr, result.Stdout)
		}
		data := NewValidExprData(resArr, at)
		bk.evalExpression(data, make([]string, 0))
		if len(bk.args) == 1 && bk.args[0] != "report" {
			bk.printTestResults(data.atb)
		} else {
			bk.AddFailedMessages(data)
		}
	}
}

func (bk *K8sAudit) addDummyCommandResponse(at *models.AuditBench, index int) string {
	spExpr := utils.SeparateExpr(at.EvalExpr)
	for _, expr := range spExpr {
		if expr.Type == common.SingleValue {
			if !strings.Contains(expr.Expr, fmt.Sprintf("'$%d'", index)) {
				if strings.Contains(expr.Expr, fmt.Sprintf("$%d", index)) {
					return common.NotValidNumber
				}
			}
		}
	}
	return common.NotValidString
}

//AddFailedMessages add failed audit test to report data
func (bk *K8sAudit) AddFailedMessages(data ValidateExprData) {
	if data.atb.TestResult.NumOfSuccess != data.atb.TestResult.NumOfExec {
		bk.FailedTests = append(bk.FailedTests, data.atb)
	}
}

//UpdateCommandParams update the cmd command with params values
func (bk *K8sAudit) UpdateCommandParams(at *models.AuditBench, index int, val string, resArr []string) string {
	params := at.CommandParams[index]
	if len(params) > 0 {
		for _, param := range params {
			x, err := strconv.Atoi(param)
			if err != nil {
				log.Console(fmt.Sprintf("failed to translate param %s to number", param))
				continue
			}
			if x < len(resArr) {
				n := resArr[x]
				switch {
				case n == "[^\"]\\S*'\n" || n == "" || n == common.NotValidString:
					return ""
				case strings.Contains(n, "\n"):
					nl := n[len(n)-1:]
					if nl == "\n" {
						n = strings.Trim(n, "\n")
					}
				}
				return strings.ReplaceAll(val, fmt.Sprintf("#%d", x), n)
			}
		}
	}
	return val
}

func (bk *K8sAudit) printTestResults(at *models.AuditBench) {
	if at.TestResult.NumOfSuccess == at.TestResult.NumOfExec {
		log.Console(emoji.Sprintf(":check_mark_button: %s\n", at.Name))
	} else {
		log.Console(emoji.Sprintf(":cross_mark: %s\n", at.Name))
	}
}

func (bk *K8sAudit) evalExpression(ved ValidateExprData, combArr []string) {
	if len(ved.resultArr) == 0 {
		return
	}
	outputs := strings.Split(ved.resultArr[0], "\n")
	for _, o := range outputs {
		if len(o) == 0 && len(outputs) > 1 {
			continue
		}
		combArr = append(combArr, o)
		bk.evalExpression(ved.NextValidExprData(), combArr)
		if ved.origSize == len(combArr) {
			expr := ved.atb.Sanitize(combArr, ved.atb.EvalExpr)
			ved.atb.TestResult.NumOfExec++
			count, err := bk.evalCommandExpr(ved.atb, expr)
			if err != nil {
				log.Console(err.Error())
			}
			ved.atb.TestResult.NumOfSuccess += count
		}
		combArr = combArr[:len(combArr)-1]
	}

}

func (bk *K8sAudit) evalCommandExpr(at *models.AuditBench, expr string) (int, error) {
	expression, err := govaluate.NewEvaluableExpression(expr)
	if err != nil {
		return 0, fmt.Errorf("failed to build evaluation command expr for\n %s", at.Name)
	}
	result, err := expression.Evaluate(nil)
	if err != nil {
		return 0, fmt.Errorf("failed to evaluate command expr for audit test %s", at.Name)
	}
	b, ok := result.(bool)
	if ok && b {
		return 1, nil
	}
	return 0, nil
}

//Synopsis for help
func (bk *K8sAudit) Synopsis() string {
	return bk.Help()
}
